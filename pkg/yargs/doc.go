// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package yargs provides a flexible, type-safe command-line argument parser with
// automatic help generation.
//
// The library is designed for building CLIs with subcommands and follows these principles:
//   - Type-safe flag parsing using Go generics
//   - Zero boilerplate for common use cases
//   - Automatic help generation from struct tags
//   - Flags can appear anywhere in the command line
//   - Consistent and predictable parsing behavior
//
// # Basic Usage (Simple CLI)
//
// For a simple CLI without subcommands, use ParseFlags:
//
//	type Flags struct {
//	    Verbose bool   `flag:"verbose" short:"v" help:"Enable verbose output"`
//	    Output  string `flag:"output" short:"o" help:"Output file"`
//	}
//
//	result, err := yargs.ParseFlags[Flags](os.Args[1:])
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Printf("Verbose: %v, Output: %s\n", result.Flags.Verbose, result.Flags.Output)
//
// # Subcommand CLI
//
// For CLIs with subcommands, use RunSubcommands with ParseAndHandleHelp in handlers:
//
//	type GlobalFlags struct {
//	    Verbose bool `flag:"verbose" short:"v" help:"Enable verbose output"`
//	}
//
//	type RunFlags struct {
//	    Detach bool `flag:"detach" short:"d" help:"Run in background"`
//	}
//
//	func handleRun(args []string) error {
//	    result, err := yargs.ParseAndHandleHelp[GlobalFlags, RunFlags](args, helpConfig)
//	    if errors.Is(err, yargs.ErrShown) {
//	        return nil
//	    }
//	    if err != nil {
//	        return err
//	    }
//	    // Use result.GlobalFlags and result.SubCommandFlags
//	    return nil
//	}
//
//	func main() {
//	    handlers := map[string]yargs.SubcommandHandler{
//	        "run": handleRun,
//	    }
//	    if err := yargs.RunSubcommands(os.Args[1:], helpConfig, GlobalFlags{}, handlers); err != nil {
//	        fmt.Fprintf(os.Stderr, "Error: %v\n", err)
//	        os.Exit(1)
//	    }
//	}
//
// # Flag Syntax
//
// Flags support both long and short forms:
//   - Boolean flags: -v, --verbose
//   - Flags with values (equals): -o=file.txt, --output=file.txt
//   - Flags with values (space): -o file.txt, --output file.txt, -n 10, --lines -5
//
// When using ParseWithCommand or ParseFlags with typed structs, the parser automatically
// consumes the next argument as the flag's value for non-boolean flags (unless the next
// argument starts with "-"). Boolean flags never consume the next argument
//
// # Supported Types
//
// The following types are supported for flag fields:
//   - string, bool
//   - int, int8, int16, int32, int64
//   - uint, uint8, uint16, uint32, uint64
//   - float32, float64
//   - time.Duration
//   - url.URL, *url.URL
//   - Port (uint16 with optional range validation via `port:"min-max"` tag)
//   - Pointer types to any of the above (e.g., *int, *bool, *string, *Port)
//
// Pointer types allow distinguishing between "flag not provided" (nil) and
// "flag provided with value" (non-nil). This is useful for optional settings
// where you need to preserve existing values if the flag is omitted.
//
// The Port type is a uint16 alias that supports optional range validation.
// Use the `port:"min-max"` struct tag to specify allowed port ranges:
//
//	type Flags struct {
//	    HTTPPort  Port  `flag:"port" port:"1-65535" help:"HTTP port"`
//	    AdminPort *Port `flag:"admin-port" port:"8000-9000" help:"Admin port"`
//	}
//
// # Command Registry and Introspection
//
// Use Registry to declare command schemas once and resolve commands without
// hardcoded lists. This is useful for checking positional requirements.
//
//	type RunArgs struct {
//	    Service string `pos:"0" help:"Service name"`
//	    Payload string `pos:"1" help:"Payload (file/image)"`
//	}
//
//	type StatusArgs struct {
//	    Service string `pos:"0?" help:"Optional service name"`
//	}
//
//	reg := yargs.Registry{
//	    Command: yargs.CommandInfo{Name: "app"},
//	    SubCommands: map[string]yargs.CommandSpec{
//	        "run": {Info: yargs.SubCommandInfo{Name: "run"}, ArgsSchema: RunArgs{}},
//	        "status": {Info: yargs.SubCommandInfo{Name: "status"}, ArgsSchema: StatusArgs{}},
//	    },
//	}
//
//	res, ok, err := yargs.ResolveCommandWithRegistry(os.Args[1:], reg)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	if ok {
//	    if arg0, ok := res.PArg(0); ok {
//	        fmt.Printf("arg0 name=%s required=%v\n", arg0.Name, arg0.Required)
//	    }
//	}
package yargs
