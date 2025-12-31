// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yargs

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Port is a uint16 type alias for IP ports with optional range validation.
// When used in struct fields, you can optionally specify allowed port ranges
// using the `port:"min-max"` struct tag. If no range is specified, all ports
// 0-65535 are allowed.
//
// Examples:
//
//	type Flags struct {
//	    HTTPPort  Port  `flag:"port" port:"1-65535" help:"HTTP port (excludes port 0)"`
//	    AdminPort *Port `flag:"admin" port:"8000-9000" help:"Admin port (restricted range)"`
//	}
//
// Note: Range validation only applies to flag fields, not positional arguments.
type Port uint16

// Help flag constants
const (
	helpFlagLong  = "--help"
	helpFlagShort = "-h"
	helpFlagLLM   = "--help-llm"
	helpCommand   = "help"
)

// Sentinel errors for help handling
var (
	// ErrHelp is returned when global help is requested (--help, -h, or "help" subcommand)
	ErrHelp = errors.New("help requested")

	// ErrSubCommandHelp is returned when subcommand-specific help is requested
	ErrSubCommandHelp = errors.New("subcommand help requested")

	// ErrHelpLLM is returned when LLM-optimized help is requested (--help-llm)
	ErrHelpLLM = errors.New("llm help requested")

	// ErrShown is returned by ParseAndHandleHelp when help or an error message was displayed.
	// This allows callers to distinguish between "message displayed successfully" and other errors.
	// Callers should treat this as a signal to exit and return nil to the user.
	ErrShown = errors.New("help or error displayed")
)

// InvalidFlagError is returned when an unknown/undefined flag is encountered.
type InvalidFlagError struct {
	Flag       string
	SubCommand string // The subcommand that had an invalid flag (if any). Empty if the flag appeared before the subcommand was identified.
}

func (e *InvalidFlagError) Error() string {
	return fmt.Sprintf("unknown flag: %s", e.Flag)
}

// InvalidArgsError is returned when the wrong number of positional arguments is provided.
type InvalidArgsError struct {
	Expected   string // "1", "1-3", "at least 1", "0 or more"
	Got        int
	SubCommand string // The subcommand that had invalid args (if any)
}

func (e *InvalidArgsError) Error() string {
	if e.SubCommand != "" {
		return fmt.Sprintf("'%s' requires %s argument(s), got %d", e.SubCommand, e.Expected, e.Got)
	}
	return fmt.Sprintf("requires %s argument(s), got %d", e.Expected, e.Got)
}

// FlagValueError is returned when a flag value cannot be parsed or is invalid.
// It provides a user-friendly error message while preserving the underlying error for debugging.
// UserMsg contains the clean user-facing error message, while Err contains the full wrapped error chain.
type FlagValueError struct {
	FlagName   string // The flag that had an invalid value (e.g., "http-port")
	FieldName  string // The struct field name (e.g., "HTTPPort")
	Value      string // The invalid value that was provided
	SubCommand string // The subcommand (if any)
	UserMsg    string // User-friendly error message for display
	Err        error  // Full wrapped error chain for debugging/verbose logging
}

func (e *FlagValueError) Error() string {
	return e.UserMsg
}

func (e *FlagValueError) Unwrap() error {
	return e.Err
}

// CommandInfo contains metadata about the CLI command for help generation.
type CommandInfo struct {
	Name            string
	Description     string
	Examples        []string
	LLMInstructions string // Additional instructions for LLMs (only shown in --help-llm)
}

// SubCommandInfo contains metadata about a subcommand for help generation.
type SubCommandInfo struct {
	Name            string
	Description     string
	Usage           string // e.g., "SERVICE" or "SERVICE [SERVICE...]"
	Examples        []string
	Aliases         []string
	Hidden          bool   // Hidden subcommands don't appear in help but still work
	LLMInstructions string // Additional instructions for LLMs (only shown in --help-llm)
}

// Group represents a collection of related subcommands with their runtime handlers.
// A group itself has no handler - it only organizes commands under a common prefix.
// Groups are used at runtime to route commands like "docker run" where "docker" is the group.
//
// Example:
//
//	groups := map[string]yargs.Group{
//	    "docker": {
//	        Description: "Docker container management",
//	        Commands: map[string]yargs.SubcommandHandler{
//	            "run":  handleDockerRun,
//	            "ps":   handleDockerPs,
//	        },
//	    },
//	}
type Group struct {
	Description string
	Commands    map[string]SubcommandHandler // Handlers for commands in this group
}

// GroupInfo contains metadata about a command group for help generation.
// Unlike Group, GroupInfo contains SubCommandInfo structs instead of handlers,
// and is used purely for generating help text. The GroupInfo should be included
// in HelpConfig.Groups and must match the structure of the Group.
//
// Example:
//
//	Groups: map[string]yargs.GroupInfo{
//	    "docker": {
//	        Name:        "docker",
//	        Description: "Docker container management",
//	        Commands: map[string]yargs.SubCommandInfo{
//	            "run": {Name: "run", Description: "Run a container"},
//	            "ps":  {Name: "ps", Description: "List containers"},
//	        },
//	    },
//	}
type GroupInfo struct {
	Name            string
	Description     string
	Commands        map[string]SubCommandInfo // Commands within this group
	Hidden          bool                      // Hidden groups don't appear in help but still work
	LLMInstructions string                    // Additional instructions for LLMs (only shown in --help-llm)
}

// HelpConfig contains all metadata needed for help generation.
type HelpConfig struct {
	Command     CommandInfo
	SubCommands map[string]SubCommandInfo // Flat subcommands (status, whoami, etc.)
	Groups      map[string]GroupInfo      // Command groups (docker, etc.)
}

// ArgSpec describes a positional argument defined by struct tags.
type ArgSpec struct {
	Position    int
	Name        string
	Type        string
	GoType      reflect.Type
	Description string
	Required    bool
	Variadic    bool
	MinCount    int
	IsSlice     bool
}

// CommandSpec describes a subcommand with optional positional-argument schema.
type CommandSpec struct {
	Info       SubCommandInfo
	ArgsSchema any
}

// GroupSpec describes a group of commands with optional positional-argument schemas.
type GroupSpec struct {
	Info     GroupInfo
	Commands map[string]CommandSpec
}

// Registry provides a schema-aware command registry independent of help config.
type Registry struct {
	Command     CommandInfo
	SubCommands map[string]CommandSpec
	Groups      map[string]GroupSpec
}

// HelpConfig returns a HelpConfig derived from the registry.
func (r Registry) HelpConfig() HelpConfig {
	subcommands := make(map[string]SubCommandInfo, len(r.SubCommands))
	for name, spec := range r.SubCommands {
		subcommands[name] = spec.Info
	}
	groups := make(map[string]GroupInfo, len(r.Groups))
	for name, group := range r.Groups {
		cmds := make(map[string]SubCommandInfo, len(group.Commands))
		for cmdName, spec := range group.Commands {
			cmds[cmdName] = spec.Info
		}
		info := group.Info
		info.Commands = cmds
		groups[name] = info
	}
	return HelpConfig{
		Command:     r.Command,
		SubCommands: subcommands,
		Groups:      groups,
	}
}

// CommandSpec returns the CommandSpec for the given command path.
func (r Registry) CommandSpec(path []string) (CommandSpec, bool) {
	if len(path) == 1 {
		spec, ok := r.SubCommands[path[0]]
		return spec, ok
	}
	if len(path) == 2 {
		group, ok := r.Groups[path[0]]
		if !ok {
			return CommandSpec{}, false
		}
		spec, ok := group.Commands[path[1]]
		return spec, ok
	}
	return CommandSpec{}, false
}

// ResolvedCommand describes a resolved subcommand and the remaining args.
// Path is ["subcommand"] or ["group", "subcommand"] and Args excludes the command tokens.
type ResolvedCommand struct {
	Path []string
	Info SubCommandInfo
	Args []string
	// ArgsSchema is an optional schema (struct with `pos` tags) used for introspection.
	ArgsSchema any
}

// PArg returns the positional argument spec at the given index, if available.
func (r ResolvedCommand) PArg(pos int) (ArgSpec, bool) {
	return ArgSpecAt(r.ArgsSchema, pos)
}

func aliasSuffix(aliases []string) string {
	if len(aliases) == 0 {
		return ""
	}
	if len(aliases) == 1 {
		return fmt.Sprintf(" (alias: %s)", aliases[0])
	}
	return fmt.Sprintf(" (aliases: %s)", strings.Join(aliases, ", "))
}

func describeWithAliases(desc string, aliases []string) string {
	suffix := aliasSuffix(aliases)
	if desc == "" {
		return strings.TrimSpace(suffix)
	}
	return desc + suffix
}

type aliasMaps struct {
	flat  map[string]string
	group map[string]map[string]string
}

func buildAliasMaps(config HelpConfig) aliasMaps {
	flat := make(map[string]string)
	group := make(map[string]map[string]string)

	for name, info := range config.SubCommands {
		for _, alias := range info.Aliases {
			if alias == "" {
				continue
			}
			if _, exists := config.SubCommands[alias]; exists {
				continue
			}
			if _, exists := config.Groups[alias]; exists {
				continue
			}
			if _, exists := flat[alias]; !exists {
				flat[alias] = name
			}
		}
	}

	for groupName, groupInfo := range config.Groups {
		for name, info := range groupInfo.Commands {
			for _, alias := range info.Aliases {
				if alias == "" {
					continue
				}
				if _, exists := groupInfo.Commands[alias]; exists {
					continue
				}
				if _, exists := group[groupName]; !exists {
					group[groupName] = make(map[string]string)
				}
				if _, exists := group[groupName][alias]; !exists {
					group[groupName][alias] = name
				}
			}
		}
	}

	return aliasMaps{flat: flat, group: group}
}

func findNonFlagArgs(args []string) []int {
	idx := make([]int, 0, 2)
	for i, arg := range args {
		if strings.HasPrefix(arg, "-") || arg == helpCommand {
			continue
		}
		idx = append(idx, i)
		if len(idx) == 2 {
			break
		}
	}
	return idx
}

// ApplyAliases rewrites args to replace any command aliases with their canonical names.
// It handles both flat subcommands and grouped commands.
func ApplyAliases(args []string, config HelpConfig) []string {
	if len(args) == 0 {
		return args
	}
	indices := findNonFlagArgs(args)
	if len(indices) == 0 {
		return args
	}

	aliases := buildAliasMaps(config)
	out := args

	firstIdx := indices[0]
	first := args[firstIdx]
	if _, isGroup := config.Groups[first]; isGroup {
		if len(indices) < 2 {
			return args
		}
		secondIdx := indices[1]
		second := args[secondIdx]
		if mapped, ok := aliases.group[first][second]; ok {
			out = append([]string{}, args...)
			out[secondIdx] = mapped
		}
		return out
	}

	if mapped, ok := aliases.flat[first]; ok {
		out = append([]string{}, args...)
		out[firstIdx] = mapped
	}
	return out
}

// extractFlagNames extracts flag names from a struct type using reflection.
// Returns a map of flag names to true for quick lookup.
// Includes both long flag names and short flag names.
func extractFlagNames(structValue reflect.Value) map[string]bool {
	flags := make(map[string]bool)
	if structValue.Kind() != reflect.Struct {
		return flags
	}

	t := structValue.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		flagName := field.Tag.Get("flag")
		if flagName == "" {
			flagName = strings.ToLower(field.Name)
		}
		flags[flagName] = true

		// Also include short flag name if present
		shortName := field.Tag.Get("short")
		if shortName != "" {
			flags[shortName] = true
		}
	}
	return flags
}

// extractFlagTypes extracts flag names and their types from a struct type using reflection.
// Returns a map of flag names to their reflect.Kind for type-aware parsing.
// Includes both long flag names and short flag names mapped to the same type.
func extractFlagTypes(structValue reflect.Value) map[string]reflect.Kind {
	flags := make(map[string]reflect.Kind)
	if structValue.Kind() != reflect.Struct {
		return flags
	}

	t := structValue.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		flagName := field.Tag.Get("flag")
		if flagName == "" {
			flagName = strings.ToLower(field.Name)
		}

		// Determine the kind we care about for parsing
		// For pointer types, unwrap all levels to get the underlying type
		fieldType := field.Type
		for fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}
		kind := fieldType.Kind()
		if kind == reflect.Slice {
			kind = reflect.Slice
		}
		flags[flagName] = kind

		// Also include short flag name if present
		shortName := field.Tag.Get("short")
		if shortName != "" {
			flags[shortName] = kind
		}
	}
	return flags
}

// Parser represents a parsed command line.
type Parser struct {
	// SubCommand is the sub-command name (first non-flag argument)
	SubCommand string
	// Args contains positional arguments to the sub-command
	Args []string
	// GlobalFlags contains global flags parsed from anywhere in the command line
	GlobalFlags map[string]string
	// SubCommandFlags contains sub-command specific flags
	SubCommandFlags map[string]string
	// Flags contains all flags (global and sub-command) for backward compatibility
	Flags map[string]string
	// RemainingArgs contains all arguments after "--" as a slice (preserves argument boundaries)
	RemainingArgs []string
}

// parse parses command-line arguments into a Parser structure without registration.
// This is an internal function that treats all flags as global flags.
// Args should not include the binary name (os.Args[1:]).
// flagTypes is an optional map of flag names to their reflect.Kind types.
// validFlags is an optional map of valid flag names. If non-nil, unknown flags will cause an error.
// expectSubCommand determines whether the first non-flag arg is treated as a subcommand.
//
// The parser supports:
//   - Sub-commands (first non-flag argument, if expectSubCommand is true)
//   - Positional arguments (non-flag arguments)
//   - Global flags that can appear anywhere before "--"
//   - Flag formats: -flag, --flag, -flag=value, --flag=value, -flag value, --flag value
//   - "--" separator to collect remaining args as a string
func parse(args []string, flagTypes map[string]reflect.Kind, validFlags map[string]bool, expectSubCommand bool) (*Parser, error) {
	p := &Parser{
		Args:  []string{},
		Flags: make(map[string]string),
	}

	var foundSubCommand bool
	var afterDoubleDash bool
	var remainingArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Handle "--" separator
		if arg == "--" {
			afterDoubleDash = true
			// Collect all remaining args
			if i+1 < len(args) {
				remainingArgs = args[i+1:]
			}
			break
		}

		// Handle flags (starting with - or --)
		if strings.HasPrefix(arg, "-") {
			// Extract flag name to look up its type
			flagName := strings.TrimLeft(arg, "-")
			if idx := strings.Index(flagName, "="); idx > 0 {
				flagName = flagName[:idx]
			}

			// Validate flag if validFlags map is provided
			if validFlags != nil && !validFlags[flagName] {
				return nil, &InvalidFlagError{Flag: "--" + flagName, SubCommand: p.SubCommand}
			}

			// Look up flag type from the provided types map
			flagType, ok := flagTypes[flagName]
			if !ok {
				// Unknown flag - default to bool for backward compatibility
				flagType = reflect.Bool
			}

			name, value, consumedNext := parseFlagWithNextArg(arg, args, i, flagType)
			if existing, ok := p.Flags[name]; ok && flagType == reflect.Slice {
				if existing == "" {
					p.Flags[name] = value
				} else if value != "" {
					p.Flags[name] = existing + "," + value
				}
			} else {
				p.Flags[name] = value
			}
			if consumedNext {
				i++ // Skip the next argument since it was used as the flag value
			}
			continue
		}

		// First non-flag argument is the sub-command (if expected)
		if expectSubCommand && !foundSubCommand {
			p.SubCommand = arg
			foundSubCommand = true
			continue
		}

		// All other non-flag arguments are positional args
		p.Args = append(p.Args, arg)
	}

	// Store remaining args after "--"
	if afterDoubleDash && len(remainingArgs) > 0 {
		p.RemainingArgs = remainingArgs
	}

	return p, nil
}

// KnownFlagsOptions controls how ParseKnownFlags handles slices.
type KnownFlagsOptions struct {
	SplitCommaSlices bool
}

// KnownFlagsResult contains parsed flags plus remaining args.
type KnownFlagsResult[T any] struct {
	Flags         T
	RemainingArgs []string
}

// ConsumeSpec describes how to consume a flag and its values from args.
// Kind is the expected type; bool flags never consume the next argument.
// SplitComma splits comma-separated values into multiple entries.
type ConsumeSpec struct {
	Kind       reflect.Kind
	SplitComma bool
}

// ConsumeFlagsBySpec removes known flags from args and returns remaining args plus parsed values.
// Unknown flags (and their following values) are left intact.
// The "--" separator stops parsing and is preserved in remaining args.
func ConsumeFlagsBySpec(args []string, specs map[string]ConsumeSpec) ([]string, map[string][]string) {
	remaining := make([]string, 0, len(args))
	values := make(map[string][]string)

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if arg == "--" {
			remaining = append(remaining, args[i:]...)
			break
		}

		if strings.HasPrefix(arg, "-") {
			flagName := strings.TrimLeft(arg, "-")
			if idx := strings.Index(flagName, "="); idx > 0 {
				flagName = flagName[:idx]
			}
			spec, ok := specs[flagName]
			if !ok {
				remaining = append(remaining, arg)
				continue
			}
			kind := spec.Kind
			if kind == 0 {
				kind = reflect.String
			}
			name, value, consumedNext := parseFlagWithNextArg(arg, args, i, kind)
			if spec.SplitComma {
				for _, part := range strings.Split(value, ",") {
					if part == "" {
						continue
					}
					values[name] = append(values[name], part)
				}
			} else {
				values[name] = append(values[name], value)
			}
			if consumedNext {
				i++
			}
			continue
		}

		remaining = append(remaining, arg)
	}

	return remaining, values
}

// ParseKnownFlags parses only the flags defined by T and leaves unknown flags intact.
// It returns the remaining args with known flags removed.
func ParseKnownFlags[T any](args []string, opts KnownFlagsOptions) (*KnownFlagsResult[T], error) {
	var flags T
	t := reflect.TypeOf(flags)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	specs := make(map[string]ConsumeSpec)
	aliases := make(map[string]string)
	kinds := make(map[string]reflect.Kind)

	if t.Kind() == reflect.Struct {
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			flagName := field.Tag.Get("flag")
			if flagName == "" {
				flagName = strings.ToLower(field.Name)
			}
			fieldType := field.Type
			for fieldType.Kind() == reflect.Ptr {
				fieldType = fieldType.Elem()
			}
			kind := fieldType.Kind()
			spec := ConsumeSpec{
				Kind:       kind,
				SplitComma: opts.SplitCommaSlices && kind == reflect.Slice,
			}
			specs[flagName] = spec
			kinds[flagName] = kind
			if short := field.Tag.Get("short"); short != "" {
				specs[short] = spec
				aliases[short] = flagName
				kinds[short] = kind
			}
		}
	}

	remaining, values := ConsumeFlagsBySpec(args, specs)
	for short, long := range aliases {
		if vals, ok := values[short]; ok {
			values[long] = append(values[long], vals...)
			delete(values, short)
		}
	}

	flat := make(map[string]string)
	for key, vals := range values {
		if len(vals) == 0 {
			continue
		}
		kind := kinds[key]
		if kind == reflect.Slice {
			flat[key] = strings.Join(vals, ",")
		} else {
			flat[key] = vals[len(vals)-1]
		}
	}

	if err := populateStruct(reflect.ValueOf(&flags).Elem(), flat); err != nil {
		return nil, err
	}
	return &KnownFlagsResult[T]{
		Flags:         flags,
		RemainingArgs: remaining,
	}, nil
}

// parseFlagWithNextArg parses a flag and checks if the next argument is its value.
// It handles formats: -flag, --flag, -flag=value, --flag=value, -flag value
//
// The flagType parameter determines whether to auto-consume the next argument:
//   - For bool flags: never auto-consume (they're standalone)
//   - For other types: auto-consume next arg if it doesn't start with "-"
//
// Returns: (name, value, consumedNext) where consumedNext indicates if the next arg was used as a value
func parseFlagWithNextArg(arg string, args []string, currentIndex int, flagType reflect.Kind) (name, value string, consumedNext bool) {
	// Remove leading dashes
	name = strings.TrimLeft(arg, "-")

	// Check if flag has an explicit value with = (--flag=value or -flag=value)
	if idx := strings.Index(name, "="); idx != -1 {
		value = name[idx+1:]
		name = name[:idx]
		return name, value, false
	}

	// For boolean flags, never consume the next argument
	if flagType == reflect.Bool {
		return name, "true", false
	}

	// For non-boolean flags, consume the next argument if it exists and doesn't look like a flag
	if currentIndex+1 < len(args) {
		nextArg := args[currentIndex+1]
		// Don't consume if the next arg looks like a flag (starts with -)
		// EXCEPT if it looks like a negative number (e.g., "-10", "-3.14")
		if !strings.HasPrefix(nextArg, "-") || isNumeric(nextArg) {
			return name, nextArg, true
		}
	}

	// No value available, return empty string (which will cause an error during type conversion for non-bool types)
	return name, "", false
}

// isNumeric checks if a string is a number (e.g., "10", "-10", "3.14", "-3.14")
func isNumeric(s string) bool {
	if len(s) == 0 {
		return false
	}

	start := 0
	// Handle optional leading sign
	if s[0] == '-' || s[0] == '+' {
		if len(s) == 1 {
			return false // Just a sign is not a number
		}
		start = 1
	}

	hasDigit := false
	hasDot := false

	for i := start; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			hasDigit = true
		} else if s[i] == '.' {
			if hasDot {
				return false // Multiple dots
			}
			hasDot = true
		} else {
			return false // Invalid character
		}
	}

	return hasDigit
}

// isNegativeNumber checks if a string is a negative number (e.g., "-10", "-3.14")
// Kept for backward compatibility with tests
func isNegativeNumber(s string) bool {
	if len(s) < 2 || s[0] != '-' {
		return false
	}
	return isNumeric(s)
}

// GetFlag returns the value of a flag, or an empty string if not set.
func (p *Parser) GetFlag(name string) string {
	return p.Flags[name]
}

// HasFlag returns true if a flag was set.
func (p *Parser) HasFlag(name string) bool {
	_, ok := p.Flags[name]
	return ok
}

// flagInfo represents information about a single flag for help generation.
type flagInfo struct {
	Name        string
	ShortName   string // Optional short name (e.g., "v" for verbose)
	Type        string
	Description string
	DefaultVal  string
}

// extractFlagInfo extracts flag information from a struct for help generation.
func extractFlagInfo(structType reflect.Type) []flagInfo {
	var flags []flagInfo

	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		flagName := field.Tag.Get("flag")
		if flagName == "" {
			flagName = strings.ToLower(field.Name)
		}

		shortName := field.Tag.Get("short")
		helpText := field.Tag.Get("help")
		defaultVal := field.Tag.Get("default")

		info := flagInfo{
			Name:        flagName,
			ShortName:   shortName,
			Type:        field.Type.String(),
			Description: helpText,
			DefaultVal:  defaultVal,
		}

		flags = append(flags, info)
	}

	return flags
}

// argInfo represents information about a positional argument for help generation and validation.
type argInfo struct {
	Position    int
	Name        string
	Type        string
	GoType      reflect.Type
	Description string
	Required    bool // true for required args
	Variadic    bool // true for []string with pos:"N*" or pos:"N+"
	MinCount    int  // minimum number of args (for variadic)
	IsSlice     bool // true if field type is []string
	FieldIndex  int  // index of the field in the struct
}

// ExtractArgSpecs extracts positional argument metadata from a struct with `pos` tags.
// It returns an empty slice if schema is nil or not a struct.
func ExtractArgSpecs(schema any) []ArgSpec {
	if schema == nil {
		return nil
	}
	t := reflect.TypeOf(schema)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	infos := extractArgsInfo(t)
	specs := make([]ArgSpec, 0, len(infos))
	for _, info := range infos {
		specs = append(specs, ArgSpec{
			Position:    info.Position,
			Name:        info.Name,
			Type:        info.Type,
			GoType:      info.GoType,
			Description: info.Description,
			Required:    info.Required,
			Variadic:    info.Variadic,
			MinCount:    info.MinCount,
			IsSlice:     info.IsSlice,
		})
	}
	return specs
}

// ArgSpecAt returns the ArgSpec for the given position in the schema, if any.
func ArgSpecAt(schema any, pos int) (ArgSpec, bool) {
	for _, spec := range ExtractArgSpecs(schema) {
		if spec.Position == pos {
			return spec, true
		}
	}
	return ArgSpec{}, false
}

// extractArgsInfo extracts positional argument information from a struct.
func extractArgsInfo(structType reflect.Type) []argInfo {
	var args []argInfo

	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		posTag := field.Tag.Get("pos")
		if posTag == "" {
			continue
		}

		helpText := field.Tag.Get("help")

		// Parse position tag
		info := argInfo{
			Name:        field.Name,
			Type:        field.Type.String(),
			GoType:      field.Type,
			Description: helpText,
			FieldIndex:  i,
		}

		// Check if field is a slice
		if field.Type.Kind() == reflect.Slice {
			info.IsSlice = true
		}

		// Parse pos tag format: "0", "0?", "0*", "0+"
		posStr := posTag
		if strings.HasSuffix(posTag, "?") {
			info.Required = false
			posStr = strings.TrimSuffix(posTag, "?")
		} else if strings.HasSuffix(posTag, "*") {
			info.Variadic = true
			info.MinCount = 0
			posStr = strings.TrimSuffix(posTag, "*")
		} else if strings.HasSuffix(posTag, "+") {
			info.Variadic = true
			info.MinCount = 1
			posStr = strings.TrimSuffix(posTag, "+")
		} else {
			info.Required = true
		}

		pos, err := strconv.Atoi(posStr)
		if err != nil {
			// Invalid position tag, skip
			continue
		}
		info.Position = pos

		args = append(args, info)
	}

	// Sort by position
	slices.SortFunc(args, func(a, b argInfo) int {
		return a.Position - b.Position
	})

	return args
}

// validateAndPopulateArgs validates positional arguments and populates the args struct.
func validateAndPopulateArgs(argsStruct reflect.Value, argsInfo []argInfo, providedArgs []string, subCommand string) error {
	if argsStruct.Kind() != reflect.Struct {
		return fmt.Errorf("args must be a struct")
	}

	// If no args info, there should be no positional args defined
	if len(argsInfo) == 0 {
		return nil
	}

	// Find variadic arg (if any)
	var variadicArg *argInfo
	requiredCount := 0
	for i := range argsInfo {
		if argsInfo[i].Variadic {
			variadicArg = &argsInfo[i]
		} else if argsInfo[i].Required {
			requiredCount++
		}
	}

	// Validate arg count
	providedCount := len(providedArgs)

	if variadicArg != nil {
		// With variadic: check minimum
		minRequired := requiredCount + variadicArg.MinCount
		if providedCount < minRequired {
			expected := fmt.Sprintf("at least %d", minRequired)
			return &InvalidArgsError{Expected: expected, Got: providedCount, SubCommand: subCommand}
		}
	} else {
		// Without variadic: exact count or optional
		maxAllowed := requiredCount
		for i := range argsInfo {
			if !argsInfo[i].Required {
				maxAllowed++
			}
		}

		if providedCount < requiredCount {
			if requiredCount == maxAllowed {
				expected := fmt.Sprintf("%d", requiredCount)
				return &InvalidArgsError{Expected: expected, Got: providedCount, SubCommand: subCommand}
			}
			expected := fmt.Sprintf("%d-%d", requiredCount, maxAllowed)
			return &InvalidArgsError{Expected: expected, Got: providedCount, SubCommand: subCommand}
		}
		if providedCount > maxAllowed {
			if requiredCount == maxAllowed {
				expected := fmt.Sprintf("%d", requiredCount)
				return &InvalidArgsError{Expected: expected, Got: providedCount, SubCommand: subCommand}
			}
			expected := fmt.Sprintf("%d-%d", requiredCount, maxAllowed)
			return &InvalidArgsError{Expected: expected, Got: providedCount, SubCommand: subCommand}
		}
	}

	// Populate struct fields
	argIndex := 0
	for _, info := range argsInfo {
		field := argsStruct.Field(info.FieldIndex)

		if info.Variadic {
			// Collect remaining args into slice
			remaining := providedArgs[argIndex:]
			if len(remaining) > 0 {
				slice := reflect.MakeSlice(field.Type(), len(remaining), len(remaining))
				for i, arg := range remaining {
					if err := setFieldValue(slice.Index(i), arg); err != nil {
						return fmt.Errorf("failed to set arg %d: %w", argIndex+i, err)
					}
				}
				field.Set(slice)
			}
			break // variadic must be last
		}

		if argIndex < len(providedArgs) {
			if err := setFieldValue(field, providedArgs[argIndex]); err != nil {
				return fmt.Errorf("failed to set arg %d: %w", argIndex, err)
			}
			argIndex++
		} else if info.Required {
			// Should have been caught by validation above
			return fmt.Errorf("missing required argument at position %d", info.Position)
		}
	}

	return nil
}

// GenerateGlobalHelp generates and prints the global help message.
func GenerateGlobalHelp[G any](config HelpConfig, globalFlagsExample G) string {
	var b strings.Builder

	// Header
	b.WriteString(config.Command.Name)
	if config.Command.Description != "" {
		b.WriteString(" - ")
		b.WriteString(config.Command.Description)
	}
	b.WriteString("\n\n")

	// Usage
	b.WriteString("USAGE:\n")
	b.WriteString(fmt.Sprintf("    %s [OPTIONS] COMMAND [ARGS...]\n\n", config.Command.Name))

	// Commands (excluding hidden commands)
	if len(config.SubCommands) > 0 {
		// Filter out hidden commands
		visibleCommands := make([]string, 0)
		for name, info := range config.SubCommands {
			if !info.Hidden {
				visibleCommands = append(visibleCommands, name)
			}
		}

		if len(visibleCommands) > 0 {
			b.WriteString("COMMANDS:\n")
			slices.Sort(visibleCommands)
			for _, name := range visibleCommands {
				info := config.SubCommands[name]
				b.WriteString(fmt.Sprintf("    %-12s %s\n", name, describeWithAliases(info.Description, info.Aliases)))
			}
			b.WriteString(fmt.Sprintf("    %-12s %s\n", helpCommand, "Show this help message"))
			b.WriteString("\n")
		}
	}

	// Command Groups (excluding hidden groups)
	if len(config.Groups) > 0 {
		// Filter out hidden groups
		visibleGroups := make([]string, 0)
		for name, info := range config.Groups {
			if !info.Hidden {
				visibleGroups = append(visibleGroups, name)
			}
		}

		if len(visibleGroups) > 0 {
			b.WriteString("COMMAND GROUPS:\n")
			slices.Sort(visibleGroups)
			for _, name := range visibleGroups {
				info := config.Groups[name]
				b.WriteString(fmt.Sprintf("    %-12s %s\n", name, info.Description))
			}
			b.WriteString("\n")
		}
	}

	// Global options
	globalFlags := extractFlagInfo(reflect.TypeOf(globalFlagsExample))
	if len(globalFlags) > 0 {
		b.WriteString("GLOBAL OPTIONS:\n")
		for _, flag := range globalFlags {
			var flagStr string
			if flag.ShortName != "" {
				flagStr = fmt.Sprintf("    -%s, --%s", flag.ShortName, flag.Name)
			} else {
				flagStr = fmt.Sprintf("    --%s", flag.Name)
			}

			if flag.Description != "" {
				b.WriteString(fmt.Sprintf("%-28s %s", flagStr, flag.Description))
			} else {
				b.WriteString(flagStr)
			}

			if flag.DefaultVal != "" {
				b.WriteString(fmt.Sprintf(" (default: %s)", flag.DefaultVal))
			}
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("%-28s %s\n", fmt.Sprintf("    %s, %s", helpFlagShort, helpFlagLong), "Show help"))
		b.WriteString(fmt.Sprintf("%-28s %s\n\n", fmt.Sprintf("        %s", helpFlagLLM), "Show LLM-optimized help"))
	}

	// Examples
	if len(config.Command.Examples) > 0 {
		b.WriteString("EXAMPLES:\n")
		for _, example := range config.Command.Examples {
			b.WriteString(fmt.Sprintf("    %s\n", example))
		}
		b.WriteString("\n")
	}

	// Footer
	if len(config.Groups) > 0 {
		b.WriteString(fmt.Sprintf("Run '%s <group>' to see commands in a group.\n", config.Command.Name))
	}
	b.WriteString(fmt.Sprintf("Run '%s COMMAND --help' for more information on a specific command.\n", config.Command.Name))

	return b.String()
}

// GenerateGroupHelp generates and prints help for a specific command group.
func GenerateGroupHelp[G any](config HelpConfig, groupName string, globalFlagsExample G) string {
	group, ok := config.Groups[groupName]
	if !ok {
		return fmt.Sprintf("Unknown command group: %s\n", groupName)
	}

	var b strings.Builder

	// Description
	if group.Description != "" {
		b.WriteString(group.Description)
		b.WriteString("\n\n")
	}

	// Usage
	b.WriteString("USAGE:\n")
	b.WriteString(fmt.Sprintf("    %s [GLOBAL OPTIONS] %s COMMAND [ARGS...]\n\n", config.Command.Name, groupName))

	// Commands in this group (excluding hidden commands)
	if len(group.Commands) > 0 {
		// Filter out hidden commands
		visibleCommands := make([]string, 0)
		for name, cmdInfo := range group.Commands {
			if !cmdInfo.Hidden {
				visibleCommands = append(visibleCommands, name)
			}
		}

		if len(visibleCommands) > 0 {
			b.WriteString("COMMANDS:\n")
			slices.Sort(visibleCommands)
			for _, name := range visibleCommands {
				cmdInfo := group.Commands[name]
				b.WriteString(fmt.Sprintf("    %-12s %s\n", name, describeWithAliases(cmdInfo.Description, cmdInfo.Aliases)))
			}
			b.WriteString("\n")
		}
	}

	// Global options
	globalFlags := extractFlagInfo(reflect.TypeOf(globalFlagsExample))
	if len(globalFlags) > 0 {
		b.WriteString("GLOBAL OPTIONS:\n")
		for _, flag := range globalFlags {
			var flagStr string
			if flag.ShortName != "" {
				flagStr = fmt.Sprintf("    -%s, --%s", flag.ShortName, flag.Name)
			} else {
				flagStr = fmt.Sprintf("    --%s", flag.Name)
			}

			if flag.Description != "" {
				b.WriteString(fmt.Sprintf("%-28s %s", flagStr, flag.Description))
			} else {
				b.WriteString(flagStr)
			}

			if flag.DefaultVal != "" {
				b.WriteString(fmt.Sprintf(" (default: %s)", flag.DefaultVal))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Footer
	b.WriteString(fmt.Sprintf("Run '%s %s COMMAND --help' for more information on a command.\n", config.Command.Name, groupName))

	return b.String()
}

// GenerateSubCommandHelp generates and prints help for a specific subcommand.
func GenerateSubCommandHelp[G any, S any, A any](config HelpConfig, subCmdName string, globalFlagsExample G, subCmdFlagsExample S, argsExample A) string {
	subCmd, ok := config.SubCommands[subCmdName]
	if !ok {
		return fmt.Sprintf("Unknown subcommand: %s\n", subCmdName)
	}

	var b strings.Builder

	// Description
	if subCmd.Description != "" {
		b.WriteString(subCmd.Description)
		b.WriteString("\n\n")
	}
	if len(subCmd.Aliases) > 0 {
		b.WriteString("**Aliases**: ")
		for i, alias := range subCmd.Aliases {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("`%s`", alias))
		}
		b.WriteString("\n\n")
	}
	if len(subCmd.Aliases) > 0 {
		b.WriteString("ALIASES:\n")
		b.WriteString(fmt.Sprintf("    %s\n\n", strings.Join(subCmd.Aliases, ", ")))
	}

	// Usage
	b.WriteString("USAGE:\n")
	usageStr := fmt.Sprintf("    %s %s [OPTIONS]", config.Command.Name, subCmdName)

	// Add positional arguments to usage
	argsInfo := extractArgsInfo(reflect.TypeOf(argsExample))
	for _, arg := range argsInfo {
		argName := strings.ToUpper(arg.Name)
		if arg.Variadic {
			if arg.MinCount > 0 {
				usageStr += fmt.Sprintf(" <%s...>", argName)
			} else {
				usageStr += fmt.Sprintf(" [%s...]", argName)
			}
		} else if arg.Required {
			usageStr += fmt.Sprintf(" <%s>", argName)
		} else {
			usageStr += fmt.Sprintf(" [%s]", argName)
		}
	}

	if subCmd.Usage != "" {
		usageStr += " " + subCmd.Usage
	}
	b.WriteString(usageStr)
	b.WriteString("\n\n")

	// Arguments section
	if len(argsInfo) > 0 {
		b.WriteString("ARGUMENTS:\n")
		for _, arg := range argsInfo {
			argName := strings.ToUpper(arg.Name)
			if arg.Description != "" {
				b.WriteString(fmt.Sprintf("    %-20s %s\n", argName, arg.Description))
			} else {
				b.WriteString(fmt.Sprintf("    %s\n", argName))
			}
		}
		b.WriteString("\n")
	}

	// Subcommand-specific options
	subCmdFlags := extractFlagInfo(reflect.TypeOf(subCmdFlagsExample))
	if len(subCmdFlags) > 0 {
		b.WriteString("OPTIONS:\n")
		for _, flag := range subCmdFlags {
			var flagStr string
			if flag.ShortName != "" {
				flagStr = fmt.Sprintf("    -%s, --%s", flag.ShortName, flag.Name)
			} else {
				flagStr = fmt.Sprintf("    --%s", flag.Name)
			}

			if flag.Description != "" {
				b.WriteString(fmt.Sprintf("%-28s %s", flagStr, flag.Description))
			} else {
				b.WriteString(flagStr)
			}

			if flag.DefaultVal != "" {
				b.WriteString(fmt.Sprintf(" (default: %s)", flag.DefaultVal))
			}
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("%-28s %s\n", fmt.Sprintf("    %s, %s", helpFlagShort, helpFlagLong), "Show this help message"))
		b.WriteString(fmt.Sprintf("%-28s %s\n\n", fmt.Sprintf("        %s", helpFlagLLM), "Show LLM-optimized help"))
	}

	// Global options
	globalFlags := extractFlagInfo(reflect.TypeOf(globalFlagsExample))
	if len(globalFlags) > 0 {
		b.WriteString("GLOBAL OPTIONS:\n")
		for _, flag := range globalFlags {
			var flagStr string
			if flag.ShortName != "" {
				flagStr = fmt.Sprintf("    -%s, --%s", flag.ShortName, flag.Name)
			} else {
				flagStr = fmt.Sprintf("    --%s", flag.Name)
			}

			if flag.Description != "" {
				b.WriteString(fmt.Sprintf("%-28s %s", flagStr, flag.Description))
			} else {
				b.WriteString(flagStr)
			}

			if flag.DefaultVal != "" {
				b.WriteString(fmt.Sprintf(" (default: %s)", flag.DefaultVal))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Examples
	if len(subCmd.Examples) > 0 {
		b.WriteString("EXAMPLES:\n")
		for _, example := range subCmd.Examples {
			b.WriteString(fmt.Sprintf("    %s\n", example))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// GenerateSubCommandHelpFromConfig generates subcommand help using only HelpConfig metadata.
// It omits subcommand-specific flags and arguments when no structured types are available.
func GenerateSubCommandHelpFromConfig[G any](config HelpConfig, subCmdName string, globalFlagsExample G) string {
	return GenerateSubCommandHelp(config, subCmdName, globalFlagsExample, struct{}{}, struct{}{})
}

// GenerateGlobalHelpLLM generates LLM-optimized help for the entire CLI as structured markdown.
// This format is designed for LLMs to understand the CLI structure, commands, and their usage.
func GenerateGlobalHelpLLM[G any](config HelpConfig, globalFlagsExample G) string {
	var b strings.Builder

	b.WriteString("# ")
	b.WriteString(config.Command.Name)
	b.WriteString(" CLI Reference\n\n")

	if config.Command.Description != "" {
		b.WriteString(config.Command.Description)
		b.WriteString("\n\n")
	}

	// LLM Instructions for the command
	if config.Command.LLMInstructions != "" {
		b.WriteString("## LLM Instructions\n\n")
		b.WriteString(config.Command.LLMInstructions)
		b.WriteString("\n\n")
	}

	// Usage
	b.WriteString("## Usage\n\n")
	b.WriteString("```\n")
	b.WriteString(fmt.Sprintf("%s [GLOBAL_OPTIONS] COMMAND [OPTIONS] [ARGS...]\n", config.Command.Name))
	b.WriteString("```\n\n")

	// Global Options
	globalFlags := extractFlagInfo(reflect.TypeOf(globalFlagsExample))
	if len(globalFlags) > 0 {
		b.WriteString("## Global Options\n\n")
		b.WriteString("These options can be used with any command:\n\n")
		for _, flag := range globalFlags {
			b.WriteString("### `--")
			b.WriteString(flag.Name)
			b.WriteString("`")
			if flag.ShortName != "" {
				b.WriteString(fmt.Sprintf(" (short: `-%s`)", flag.ShortName))
			}
			b.WriteString("\n\n")

			if flag.Description != "" {
				b.WriteString(flag.Description)
				b.WriteString("\n\n")
			}

			b.WriteString(fmt.Sprintf("- **Type**: `%s`\n", flag.Type))
			if flag.DefaultVal != "" {
				b.WriteString(fmt.Sprintf("- **Default**: `%s`\n", flag.DefaultVal))
			}
			b.WriteString("\n")
		}
	}

	// Commands
	if len(config.SubCommands) > 0 {
		// Get visible commands
		visibleCommands := make([]string, 0)
		for name, info := range config.SubCommands {
			if !info.Hidden {
				visibleCommands = append(visibleCommands, name)
			}
		}

		if len(visibleCommands) > 0 {
			b.WriteString("## Commands\n\n")
			slices.Sort(visibleCommands)
			for _, name := range visibleCommands {
				info := config.SubCommands[name]
				b.WriteString(fmt.Sprintf("### `%s`\n\n", name))
				if info.Description != "" {
					b.WriteString(info.Description)
					b.WriteString("\n\n")
				}
				if len(info.Aliases) > 0 {
					b.WriteString("**Aliases**: ")
					for i, alias := range info.Aliases {
						if i > 0 {
							b.WriteString(", ")
						}
						b.WriteString(fmt.Sprintf("`%s`", alias))
					}
					b.WriteString("\n\n")
				}
				if info.LLMInstructions != "" {
					b.WriteString("**LLM Instructions**: ")
					b.WriteString(info.LLMInstructions)
					b.WriteString("\n\n")
				}
				if len(info.Examples) > 0 {
					b.WriteString("**Examples**:\n\n")
					for _, example := range info.Examples {
						b.WriteString(fmt.Sprintf("```\n%s\n```\n\n", example))
					}
				}
				b.WriteString(fmt.Sprintf("Get detailed help: `%s %s --help-llm`\n\n", config.Command.Name, name))
			}
		}
	}

	// Command Groups
	if len(config.Groups) > 0 {
		// Get visible groups
		visibleGroups := make([]string, 0)
		for name, info := range config.Groups {
			if !info.Hidden {
				visibleGroups = append(visibleGroups, name)
			}
		}

		if len(visibleGroups) > 0 {
			b.WriteString("## Command Groups\n\n")
			slices.Sort(visibleGroups)
			for _, name := range visibleGroups {
				info := config.Groups[name]
				b.WriteString(fmt.Sprintf("### `%s`\n\n", name))
				if info.Description != "" {
					b.WriteString(info.Description)
					b.WriteString("\n\n")
				}
				if info.LLMInstructions != "" {
					b.WriteString("**LLM Instructions**: ")
					b.WriteString(info.LLMInstructions)
					b.WriteString("\n\n")
				}

				// List commands in this group
				if len(info.Commands) > 0 {
					visibleGroupCommands := make([]string, 0)
					for cmdName, cmdInfo := range info.Commands {
						if !cmdInfo.Hidden {
							visibleGroupCommands = append(visibleGroupCommands, cmdName)
						}
					}
					if len(visibleGroupCommands) > 0 {
						b.WriteString("**Commands**:\n\n")
						slices.Sort(visibleGroupCommands)
						for _, cmdName := range visibleGroupCommands {
							cmdInfo := info.Commands[cmdName]
							desc := describeWithAliases(cmdInfo.Description, cmdInfo.Aliases)
							b.WriteString(fmt.Sprintf("- `%s %s`: %s\n", name, cmdName, desc))
						}
						b.WriteString("\n")
					}
				}
				b.WriteString(fmt.Sprintf("Get detailed help: `%s %s --help-llm`\n\n", config.Command.Name, name))
			}
		}
	}

	// Examples
	if len(config.Command.Examples) > 0 {
		b.WriteString("## Examples\n\n")
		for _, example := range config.Command.Examples {
			b.WriteString(fmt.Sprintf("```\n%s\n```\n\n", example))
		}
	}

	// Footer
	b.WriteString("## Getting Help\n\n")
	b.WriteString(fmt.Sprintf("- Global help: `%s --help-llm`\n", config.Command.Name))
	if len(config.Groups) > 0 {
		b.WriteString(fmt.Sprintf("- Group help: `%s <group> --help-llm`\n", config.Command.Name))
	}
	b.WriteString(fmt.Sprintf("- Command help: `%s <command> --help-llm`\n", config.Command.Name))

	return b.String()
}

// GenerateGroupHelpLLM generates LLM-optimized help for a specific command group as structured markdown.
func GenerateGroupHelpLLM[G any](config HelpConfig, groupName string, globalFlagsExample G) string {
	group, ok := config.Groups[groupName]
	if !ok {
		return fmt.Sprintf("# Unknown Group: %s\n\nGroup not found.\n", groupName)
	}

	var b strings.Builder

	b.WriteString(fmt.Sprintf("# %s - %s\n\n", config.Command.Name, groupName))

	if group.Description != "" {
		b.WriteString(group.Description)
		b.WriteString("\n\n")
	}

	// LLM Instructions for the group
	if group.LLMInstructions != "" {
		b.WriteString("## LLM Instructions\n\n")
		b.WriteString(group.LLMInstructions)
		b.WriteString("\n\n")
	}

	// Usage
	b.WriteString("## Usage\n\n")
	b.WriteString("```\n")
	b.WriteString(fmt.Sprintf("%s [GLOBAL_OPTIONS] %s COMMAND [OPTIONS] [ARGS...]\n", config.Command.Name, groupName))
	b.WriteString("```\n\n")

	// Global Options
	globalFlags := extractFlagInfo(reflect.TypeOf(globalFlagsExample))
	if len(globalFlags) > 0 {
		b.WriteString("## Global Options\n\n")
		for _, flag := range globalFlags {
			b.WriteString("### `--")
			b.WriteString(flag.Name)
			b.WriteString("`")
			if flag.ShortName != "" {
				b.WriteString(fmt.Sprintf(" (short: `-%s`)", flag.ShortName))
			}
			b.WriteString("\n\n")

			if flag.Description != "" {
				b.WriteString(flag.Description)
				b.WriteString("\n\n")
			}

			b.WriteString(fmt.Sprintf("- **Type**: `%s`\n", flag.Type))
			if flag.DefaultVal != "" {
				b.WriteString(fmt.Sprintf("- **Default**: `%s`\n", flag.DefaultVal))
			}
			b.WriteString("\n")
		}
	}

	// Commands in this group
	if len(group.Commands) > 0 {
		visibleCommands := make([]string, 0)
		for name, cmdInfo := range group.Commands {
			if !cmdInfo.Hidden {
				visibleCommands = append(visibleCommands, name)
			}
		}

		if len(visibleCommands) > 0 {
			b.WriteString("## Commands\n\n")
			slices.Sort(visibleCommands)
			for _, name := range visibleCommands {
				cmdInfo := group.Commands[name]
				b.WriteString(fmt.Sprintf("### `%s %s`\n\n", groupName, name))
				if cmdInfo.Description != "" {
					b.WriteString(cmdInfo.Description)
					b.WriteString("\n\n")
				}
				if cmdInfo.LLMInstructions != "" {
					b.WriteString("**LLM Instructions**: ")
					b.WriteString(cmdInfo.LLMInstructions)
					b.WriteString("\n\n")
				}
				if len(cmdInfo.Examples) > 0 {
					b.WriteString("**Examples**:\n\n")
					for _, example := range cmdInfo.Examples {
						b.WriteString(fmt.Sprintf("```\n%s\n```\n\n", example))
					}
				}
				b.WriteString(fmt.Sprintf("Get detailed help: `%s %s %s --help-llm`\n\n", config.Command.Name, groupName, name))
			}
		}
	}

	return b.String()
}

// GenerateSubCommandHelpLLM generates LLM-optimized help for a specific subcommand as structured markdown.
func GenerateSubCommandHelpLLM[G any, S any, A any](config HelpConfig, subCmdName string, globalFlagsExample G, subCmdFlagsExample S, argsExample A) string {
	subCmd, ok := config.SubCommands[subCmdName]
	if !ok {
		return fmt.Sprintf("# Unknown Command: %s\n\nCommand not found.\n", subCmdName)
	}

	var b strings.Builder

	b.WriteString(fmt.Sprintf("# %s %s\n\n", config.Command.Name, subCmdName))

	if subCmd.Description != "" {
		b.WriteString(subCmd.Description)
		b.WriteString("\n\n")
	}

	// LLM Instructions for the subcommand
	if subCmd.LLMInstructions != "" {
		b.WriteString("## LLM Instructions\n\n")
		b.WriteString(subCmd.LLMInstructions)
		b.WriteString("\n\n")
	}

	// Usage
	b.WriteString("## Usage\n\n")
	b.WriteString("```\n")
	usageStr := fmt.Sprintf("%s [GLOBAL_OPTIONS] %s", config.Command.Name, subCmdName)

	// Add positional arguments to usage
	argsInfo := extractArgsInfo(reflect.TypeOf(argsExample))
	if len(argsInfo) > 0 {
		for _, arg := range argsInfo {
			argName := strings.ToUpper(arg.Name)
			if arg.Variadic {
				if arg.MinCount > 0 {
					usageStr += fmt.Sprintf(" <%s...>", argName)
				} else {
					usageStr += fmt.Sprintf(" [%s...]", argName)
				}
			} else if arg.Required {
				usageStr += fmt.Sprintf(" <%s>", argName)
			} else {
				usageStr += fmt.Sprintf(" [%s]", argName)
			}
		}
	}

	usageStr += " [OPTIONS]"
	if subCmd.Usage != "" {
		usageStr += " " + subCmd.Usage
	}
	b.WriteString(usageStr)
	b.WriteString("\n```\n\n")

	// Arguments section
	if len(argsInfo) > 0 {
		b.WriteString("## Arguments\n\n")
		for _, arg := range argsInfo {
			argName := strings.ToUpper(arg.Name)
			b.WriteString(fmt.Sprintf("### `%s`\n\n", argName))
			if arg.Description != "" {
				b.WriteString(arg.Description)
				b.WriteString("\n\n")
			}
			b.WriteString(fmt.Sprintf("- **Type**: `%s`\n", arg.Type))
			b.WriteString(fmt.Sprintf("- **Required**: %v\n", arg.Required))
			if arg.Variadic {
				b.WriteString(fmt.Sprintf("- **Variadic**: true (minimum: %d)\n", arg.MinCount))
			}
			b.WriteString("\n")
		}
	}

	// Subcommand-specific options
	subCmdFlags := extractFlagInfo(reflect.TypeOf(subCmdFlagsExample))
	if len(subCmdFlags) > 0 {
		b.WriteString("## Options\n\n")
		for _, flag := range subCmdFlags {
			b.WriteString("### `--")
			b.WriteString(flag.Name)
			b.WriteString("`")
			if flag.ShortName != "" {
				b.WriteString(fmt.Sprintf(" (short: `-%s`)", flag.ShortName))
			}
			b.WriteString("\n\n")

			if flag.Description != "" {
				b.WriteString(flag.Description)
				b.WriteString("\n\n")
			}

			b.WriteString(fmt.Sprintf("- **Type**: `%s`\n", flag.Type))
			if flag.DefaultVal != "" {
				b.WriteString(fmt.Sprintf("- **Default**: `%s`\n", flag.DefaultVal))
			}
			b.WriteString("\n")
		}
	}

	// Global options
	globalFlags := extractFlagInfo(reflect.TypeOf(globalFlagsExample))
	if len(globalFlags) > 0 {
		b.WriteString("## Global Options\n\n")
		for _, flag := range globalFlags {
			b.WriteString("### `--")
			b.WriteString(flag.Name)
			b.WriteString("`")
			if flag.ShortName != "" {
				b.WriteString(fmt.Sprintf(" (short: `-%s`)", flag.ShortName))
			}
			b.WriteString("\n\n")

			if flag.Description != "" {
				b.WriteString(flag.Description)
				b.WriteString("\n\n")
			}

			b.WriteString(fmt.Sprintf("- **Type**: `%s`\n", flag.Type))
			if flag.DefaultVal != "" {
				b.WriteString(fmt.Sprintf("- **Default**: `%s`\n", flag.DefaultVal))
			}
			b.WriteString("\n")
		}
	}

	// Examples
	if len(subCmd.Examples) > 0 {
		b.WriteString("## Examples\n\n")
		for _, example := range subCmd.Examples {
			b.WriteString(fmt.Sprintf("```\n%s\n```\n\n", example))
		}
	}

	return b.String()
}

// GenerateSubCommandHelpLLMFromConfig generates LLM-optimized help for a subcommand
// using only HelpConfig metadata.
func GenerateSubCommandHelpLLMFromConfig[G any](config HelpConfig, subCmdName string, globalFlagsExample G) string {
	return GenerateSubCommandHelpLLM(config, subCmdName, globalFlagsExample, struct{}{}, struct{}{})
}

// ParseFlags parses command-line arguments and returns a typed struct with parsed flags.
// The struct fields should have a `flag:"name"` tag to specify the flag name.
// Optionally use `short:"x"` tag for short flag aliases.
// Supported types: string, bool, int, int64, uint, uint64, float64, time.Duration, url.URL, *url.URL
// Pointer types (*int, *bool, *string, etc.) are also supported for optional flags - they remain nil if not specified
//
// Example:
//
//	type Flags struct {
//	    Verbose    bool   `flag:"verbose" short:"v"`
//	    ControlURL string `flag:"control-url"`
//	    Port       int    `flag:"port"`
//	}
//
//	result, err := yargs.ParseFlags[Flags](os.Args[1:])
func ParseFlags[T any](args []string) (*ParseResult[T], error) {
	// Extract flag types and names from the struct
	var flags T
	flagTypes := extractFlagTypes(reflect.ValueOf(flags))
	validFlags := extractFlagNames(reflect.ValueOf(flags))

	// ParseFlags is for simple programs without subcommands - all non-flag args are positional
	p, err := parse(args, flagTypes, validFlags, false)
	if err != nil {
		return nil, err
	}
	if err := populateStruct(reflect.ValueOf(&flags).Elem(), p.Flags); err != nil {
		return nil, err
	}
	return &ParseResult[T]{
		Parser: p,
		Flags:  flags,
	}, nil
}

// ParseResult combines the parsed flags with the Parser for access to other fields.
type ParseResult[T any] struct {
	*Parser
	Flags T
}

// ParseWithCommand parses command-line arguments and returns typed structs for global flags, subcommand flags, and positional args.
// The generic type parameters define which flags are global vs subcommand-specific via struct tags.
// Panics if the same flag name appears in both G and S types.
func ParseWithCommand[G any, S any, A any](args []string) (*TypedParseResult[G, S, A], error) {
	// Check for help flags before parsing
	if len(args) > 0 {
		// Check for global help
		if args[0] == helpCommand || args[0] == helpFlagLong || args[0] == helpFlagShort {
			return nil, ErrHelp
		}

		// Check for subcommand help
		if len(args) > 1 {
			for i := 1; i < len(args); i++ {
				if args[i] == helpFlagLong || args[i] == helpFlagShort {
					return nil, ErrSubCommandHelp
				}
			}
		}
	}

	// Extract flag names and types from the generic types to determine categorization
	var globalFlags G
	var subCmdFlags S
	globalFlagNames := extractFlagNames(reflect.ValueOf(globalFlags))
	subCmdFlagNames := extractFlagNames(reflect.ValueOf(subCmdFlags))
	globalFlagTypes := extractFlagTypes(reflect.ValueOf(globalFlags))
	subCmdFlagTypes := extractFlagTypes(reflect.ValueOf(subCmdFlags))

	// Combine type maps for lookup during parsing
	allFlagTypes := make(map[string]reflect.Kind)
	for name, kind := range globalFlagTypes {
		allFlagTypes[name] = kind
	}
	for name, kind := range subCmdFlagTypes {
		allFlagTypes[name] = kind
	}

	// Combine valid flag names for validation
	allValidFlags := make(map[string]bool)
	for name := range globalFlagNames {
		allValidFlags[name] = true
	}
	for name := range subCmdFlagNames {
		allValidFlags[name] = true
	}

	// Check for conflicts between global and subcommand flags
	for flagName := range globalFlagNames {
		if subCmdFlagNames[flagName] {
			panic(fmt.Sprintf("flag %q is defined in both global and subcommand flag types", flagName))
		}
	}

	// Use the common parse() function with expectSubCommand=true
	p, err := parse(args, allFlagTypes, allValidFlags, true)
	if err != nil {
		return nil, err
	}

	// Categorize the parsed flags into GlobalFlags and SubCommandFlags
	p.GlobalFlags = make(map[string]string)
	p.SubCommandFlags = make(map[string]string)

	for name, value := range p.Flags {
		if globalFlagNames[name] {
			p.GlobalFlags[name] = value
		} else if subCmdFlagNames[name] {
			p.SubCommandFlags[name] = value
		} else {
			// Unregistered flag, treat as global for flexibility
			p.GlobalFlags[name] = value
		}
	}

	// Populate the typed structs
	if err := populateStruct(reflect.ValueOf(&globalFlags).Elem(), p.GlobalFlags); err != nil {
		// If it's a FlagValueError, set the SubCommand field and wrap with context
		var flagErr *FlagValueError
		if errors.As(err, &flagErr) {
			flagErr.SubCommand = p.SubCommand
			// Wrap the error with parsing context
			flagErr.Err = fmt.Errorf("failed to parse sub-command flags: %w", flagErr.Err)
		}
		return nil, err
	}

	if err := populateStruct(reflect.ValueOf(&subCmdFlags).Elem(), p.SubCommandFlags); err != nil {
		// If it's a FlagValueError, set the SubCommand field and wrap with context
		var flagErr *FlagValueError
		if errors.As(err, &flagErr) {
			flagErr.SubCommand = p.SubCommand
			// Wrap the error with parsing context
			flagErr.Err = fmt.Errorf("failed to parse sub-command flags: %w", flagErr.Err)
		}
		return nil, err
	}

	// Validate and populate positional arguments
	var argsStruct A
	argsInfo := extractArgsInfo(reflect.TypeOf(argsStruct))
	if err := validateAndPopulateArgs(reflect.ValueOf(&argsStruct).Elem(), argsInfo, p.Args, p.SubCommand); err != nil {
		return nil, err
	}

	return &TypedParseResult[G, S, A]{
		Parser:          p,
		GlobalFlags:     globalFlags,
		SubCommandFlags: subCmdFlags,
		Args:            argsStruct,
	}, nil
}

// TypedParseResult combines the parsed flags and args with the Parser for access to other fields.
type TypedParseResult[G any, S any, A any] struct {
	*Parser
	GlobalFlags     G
	SubCommandFlags S
	Args            A
	HelpText        string // Contains generated help text if help was requested
}

// ParseWithCommandAndHelp parses command-line arguments with automatic help generation.
// If help is requested (via --help, -h, or help subcommand), it generates the help text
// and returns it in the HelpText field along with the appropriate error (ErrHelp or ErrSubCommandHelp).
func ParseWithCommandAndHelp[G any, S any, A any](args []string, config HelpConfig) (*TypedParseResult[G, S, A], error) {
	var globalFlags G
	var subCmdFlags S
	var argsStruct A

	// Check for help flags before parsing
	if len(args) > 0 {
		// Check for global --help-llm
		if args[0] == helpFlagLLM {
			helpText := GenerateGlobalHelpLLM(config, globalFlags)
			return &TypedParseResult[G, S, A]{
				HelpText: helpText,
			}, ErrHelpLLM
		}
		args = ApplyAliases(args, config)

		// Check for "help SUBCOMMAND" pattern first (more specific)
		if args[0] == helpCommand && len(args) > 1 {
			subCmdName := args[1]
			helpText := GenerateSubCommandHelp(config, subCmdName, globalFlags, subCmdFlags, argsStruct)
			return &TypedParseResult[G, S, A]{
				HelpText: helpText,
			}, ErrSubCommandHelp
		}

		// Check for global help
		if args[0] == helpCommand || args[0] == helpFlagLong || args[0] == helpFlagShort {
			helpText := GenerateGlobalHelp(config, globalFlags)
			return &TypedParseResult[G, S, A]{
				HelpText: helpText,
			}, ErrHelp
		}

		// Check for subcommand help (e.g., "run --help" or "run --help-llm")
		if len(args) > 1 {
			// Find the subcommand
			var subCmdName string
			for _, arg := range args {
				if !strings.HasPrefix(arg, "-") {
					subCmdName = arg
					break
				}
			}

			// Check if help is requested for this subcommand
			for i := 1; i < len(args); i++ {
				if args[i] == helpFlagLLM {
					helpText := GenerateSubCommandHelpLLM(config, subCmdName, globalFlags, subCmdFlags, argsStruct)
					return &TypedParseResult[G, S, A]{
						HelpText: helpText,
					}, ErrHelpLLM
				}
				if args[i] == helpFlagLong || args[i] == helpFlagShort {
					helpText := GenerateSubCommandHelp(config, subCmdName, globalFlags, subCmdFlags, argsStruct)
					return &TypedParseResult[G, S, A]{
						HelpText: helpText,
					}, ErrSubCommandHelp
				}
			}
		}
	}

	// No help requested, parse normally
	result, err := ParseWithCommand[G, S, A](args)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// ExtractSubcommand extracts the subcommand name from command-line arguments.
// It returns the first non-flag argument that isn't "help".
// Returns empty string if no subcommand is found.
func ExtractSubcommand(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") && arg != helpCommand {
			return arg
		}
	}
	return ""
}

// ExtractGroupAndSubcommand extracts both group and subcommand from command-line arguments.
// It returns the first two non-flag arguments (excluding "help").
// Returns empty strings if not found.
// Example: ["docker", "run"] -> ("docker", "run")
// Example: ["status"] -> ("", "status")
func ExtractGroupAndSubcommand(args []string) (group, subcommand string) {
	var found []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") && arg != helpCommand {
			found = append(found, arg)
			if len(found) == 2 {
				return found[0], found[1]
			}
		}
	}

	if len(found) == 1 {
		return "", found[0]
	}
	return "", ""
}

// ResolveCommand resolves a subcommand (including grouped commands) using the same
// alias and parsing rules as the dispatcher. It returns the resolved command path
// and the remaining args with the command tokens removed.
func ResolveCommand(args []string, config HelpConfig) (ResolvedCommand, bool, error) {
	if len(args) == 0 {
		return ResolvedCommand{}, false, nil
	}
	if args[0] == helpCommand || args[0] == helpFlagLong || args[0] == helpFlagShort || args[0] == helpFlagLLM {
		return ResolvedCommand{}, false, nil
	}
	args = ApplyAliases(args, config)
	first, second := ExtractGroupAndSubcommand(args)
	if first == "" && second == "" {
		return ResolvedCommand{}, false, nil
	}

	if first != "" && second != "" {
		if grp, ok := config.Groups[first]; ok {
			if cmd, ok := grp.Commands[second]; ok {
				trimmed := stripFirstNonFlagArg(args)
				trimmed = stripFirstNonFlagArg(trimmed)
				return ResolvedCommand{Path: []string{first, second}, Info: cmd, Args: trimmed}, true, nil
			}
			return ResolvedCommand{}, false, fmt.Errorf("unknown command in group '%s': %s", first, second)
		}
	}

	cmdName := first
	if cmdName == "" {
		cmdName = second
	}
	if cmdName == "" {
		return ResolvedCommand{}, false, nil
	}
	if _, isGroup := config.Groups[cmdName]; isGroup {
		return ResolvedCommand{}, false, nil
	}
	if cmd, ok := config.SubCommands[cmdName]; ok {
		trimmed := stripFirstNonFlagArg(args)
		return ResolvedCommand{Path: []string{cmdName}, Info: cmd, Args: trimmed}, true, nil
	}
	return ResolvedCommand{}, false, fmt.Errorf("unknown command: %s", cmdName)
}

// ResolveCommandWithRegistry resolves a command using the registry and attaches the args schema.
func ResolveCommandWithRegistry(args []string, reg Registry) (ResolvedCommand, bool, error) {
	res, ok, err := ResolveCommand(args, reg.HelpConfig())
	if err != nil || !ok {
		return res, ok, err
	}
	if spec, ok := reg.CommandSpec(res.Path); ok {
		res.ArgsSchema = spec.ArgsSchema
	}
	return res, ok, err
}

// SubcommandHandler is a function that handles a subcommand.
// It receives the original args (including the subcommand name) and returns an error if the command fails.
type SubcommandHandler func(context.Context, []string) error

// isHelpFlag returns true if the argument is a help request.
func isHelpFlag(arg string) bool {
	return arg == helpCommand || arg == helpFlagLong || arg == helpFlagShort
}

func helpFlagsInArgs(args []string) (help bool, helpLLM bool) {
	for _, arg := range args {
		if arg == helpFlagLLM {
			helpLLM = true
			continue
		}
		if arg == helpFlagLong || arg == helpFlagShort {
			help = true
		}
	}
	return help, helpLLM
}

// stripFirstNonFlagArg removes the first non-flag argument from args.
// This is used for grouped commands to strip the group name before passing to handlers.
// The "help" check ensures that "help docker" doesn't strip "docker" from the args.
//
// Examples:
//   - ["docker", "run", "nginx"] -> ["run", "nginx"]
//   - ["-v", "docker", "run", "nginx"] -> ["-v", "run", "nginx"]
//   - ["help", "docker", "run"] -> ["help", "docker", "run"] (unchanged)
func stripFirstNonFlagArg(args []string) []string {
	for i, arg := range args {
		if !strings.HasPrefix(arg, "-") && arg != helpCommand {
			// Found first non-flag arg (not help), return args without it
			result := make([]string, 0, len(args)-1)
			result = append(result, args[:i]...)
			result = append(result, args[i+1:]...)
			return result
		}
	}
	return args
}

// RunSubcommands automatically routes to the appropriate subcommand handler.
// It handles:
//   - Empty args: shows global help
//   - Global help flags (--help, -h, help): shows global help
//   - Unknown subcommands: returns error with helpful message
//   - Known subcommands: calls the registered handler
//
// This eliminates all the boilerplate of manual help handling and routing.
//
// Example:
//
//	handlers := map[string]yargs.SubcommandHandler{
//	    "status": handleStatus,
//	    "run":    handleRun,
//	}
//	if err := yargs.RunSubcommands(os.Args[1:], helpConfig, GlobalFlags{}, handlers); err != nil {
//	    fmt.Fprintf(os.Stderr, "Error: %v\n", err)
//	    os.Exit(1)
//	}
func RunSubcommands[G any](ctx context.Context, args []string, config HelpConfig, globalFlagsExample G, handlers map[string]SubcommandHandler) error {
	// Delegate to RunSubcommandsWithGroups with no groups
	return RunSubcommandsWithGroups(ctx, args, config, globalFlagsExample, handlers, nil)
}

// RunSubcommandsWithGroups automatically routes to the appropriate subcommand handler,
// supporting both flat commands and grouped commands (e.g., "docker run").
// It handles:
//   - Empty args: shows global help
//   - Global help flags (--help, -h, help): shows global help
//   - Group without subcommand (e.g., "docker"): shows group help
//   - Group help flags (e.g., "docker --help"): shows group help
//   - Grouped subcommands (e.g., "docker run"): calls the handler from the group
//   - Flat subcommands (e.g., "status"): calls the handler from commands map
//   - Unknown groups/commands: returns error with helpful message
//
// This allows mixing flat commands with grouped commands for better organization.
//
// Example:
//
//	commands := map[string]yargs.SubcommandHandler{
//	    "status": handleStatus,
//	    "whoami": handleWhoAmI,
//	}
//	groups := map[string]yargs.Group{
//	    "docker": {
//	        Description: "Docker-related commands",
//	        Commands: map[string]yargs.SubcommandHandler{
//	            "run":  handleDockerRun,
//	            "ps":   handleDockerPs,
//	        },
//	    },
//	}
//	if err := yargs.RunSubcommandsWithGroups(ctx, os.Args[1:], config, GlobalFlags{}, commands, groups); err != nil {
//	    fmt.Fprintf(os.Stderr, "Error: %v\n", err)
//	    os.Exit(1)
//	}
func RunSubcommandsWithGroups[G any](ctx context.Context, args []string, config HelpConfig, globalFlagsExample G, commands map[string]SubcommandHandler, groups map[string]Group) error {
	// Handle empty args or global help
	if len(args) == 0 || isHelpFlag(args[0]) {
		fmt.Print(GenerateGlobalHelp(config, globalFlagsExample))
		return nil
	}

	// Check for global --help-llm
	if args[0] == helpFlagLLM {
		fmt.Print(GenerateGlobalHelpLLM(config, globalFlagsExample))
		return nil
	}

	args = ApplyAliases(args, config)

	// Validate no conflicts between flat commands and groups
	for groupName := range groups {
		if _, exists := commands[groupName]; exists {
			panic(fmt.Sprintf("command name %q conflicts with group name", groupName))
		}
	}

	// Extract potential group and subcommand
	// Returns ("group", "subcommand") for "docker run"
	// Returns ("", "command") for "status"
	// Returns ("", "") for empty or only flags
	first, second := ExtractGroupAndSubcommand(args)
	if first == "" && second == "" {
		return fmt.Errorf("unknown command: %s\nRun '%s --help' for usage", args[0], config.Command.Name)
	}

	// Case 1: Two non-flag args (potential group + subcommand)
	if first != "" && second != "" {
		// Check if first arg is a group
		if grp, isGroup := groups[first]; isGroup {
			// Check if help is requested
			for _, arg := range args[1:] {
				if arg == helpFlagLLM {
					fmt.Print(GenerateGroupHelpLLM(config, first, globalFlagsExample))
					return nil
				}
				if isHelpFlag(arg) {
					fmt.Print(GenerateGroupHelp(config, first, globalFlagsExample))
					return nil
				}
			}
			// Route to subcommand within group
			handler, ok := grp.Commands[second]
			if !ok {
				return fmt.Errorf("unknown command in group '%s': %s\nRun '%s %s' for usage", first, second, config.Command.Name, first)
			}
			// Strip the group name from args before passing to handler
			// e.g., ["docker", "run", "nginx"] -> ["run", "nginx"]
			argsWithoutGroup := stripFirstNonFlagArg(args)
			return handler(ctx, argsWithoutGroup)
		}
		// Not a group - first arg must be a flat command, second is a positional arg
		// Fall through to flat command handling
	}

	// Case 2: One non-flag arg (could be group or flat command)
	cmdName := first
	if cmdName == "" {
		cmdName = second
	}

	// Check if it's a group (show group help)
	if _, isGroup := groups[cmdName]; isGroup {
		// Check for --help-llm flag
		for _, arg := range args {
			if arg == helpFlagLLM {
				fmt.Print(GenerateGroupHelpLLM(config, cmdName, globalFlagsExample))
				return nil
			}
		}
		fmt.Print(GenerateGroupHelp(config, cmdName, globalFlagsExample))
		return nil
	}

	// Not a group - must be a flat command
	handler, ok := commands[cmdName]
	if !ok {
		return fmt.Errorf("unknown command: %s\nRun '%s --help' for usage", cmdName, config.Command.Name)
	}
	if help, helpLLM := helpFlagsInArgs(args); help || helpLLM {
		if helpLLM {
			fmt.Print(GenerateSubCommandHelpLLMFromConfig(config, cmdName, globalFlagsExample))
			return nil
		}
		fmt.Print(GenerateSubCommandHelpFromConfig(config, cmdName, globalFlagsExample))
		return nil
	}
	return handler(ctx, args)
}

// isVerboseEnabled checks if verbose mode is enabled by looking for a "Verbose" field in the global flags struct.
// It uses reflection to check if the struct has a Verbose field of type bool and if it's set to true.
func isVerboseEnabled[G any](globalFlags G) bool {
	v := reflect.ValueOf(globalFlags)
	if v.Kind() != reflect.Struct {
		return false
	}

	// Look for a field named "Verbose" (case-sensitive)
	verboseField := v.FieldByName("Verbose")
	if !verboseField.IsValid() {
		return false
	}

	// Check if it's a bool type
	if verboseField.Kind() != reflect.Bool {
		return false
	}

	return verboseField.Bool()
}

// ParseAndHandleHelp parses command-line arguments and automatically handles help output.
// If help is requested, it prints the help text to stdout and returns (nil, ErrShown).
// This eliminates the need for manual help checking in command handlers.
//
// When a FlagValueError occurs and the global flags struct has a "Verbose" field set to true,
// detailed error information is displayed including the flag name, value, subcommand, and full error chain.
//
// Returns:
//   - (result, nil) on successful parse
//   - (nil, ErrShown) if help was displayed (caller should treat as success)
//   - (nil, error) if parsing failed
//
// Usage:
//
//	result, err := yargs.ParseAndHandleHelp[GlobalFlags, RunFlags, RunArgs](args, config)
//	if errors.Is(err, yargs.ErrShown) {
//	    return nil // Help was shown, exit successfully
//	}
//	if err != nil {
//	    return err // Actual error
//	}
//	// Use result.GlobalFlags, result.SubCommandFlags, and result.Args
func ParseAndHandleHelp[G any, S any, A any](args []string, config HelpConfig) (*TypedParseResult[G, S, A], error) {
	result, err := ParseWithCommandAndHelp[G, S, A](args, config)
	if err != nil {
		if errors.Is(err, ErrHelp) || errors.Is(err, ErrSubCommandHelp) || errors.Is(err, ErrHelpLLM) {
			fmt.Print(result.HelpText)
			return nil, ErrShown
		}
		// For InvalidArgsError, print the error message with help hint
		var argsErr *InvalidArgsError
		if errors.As(err, &argsErr) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if argsErr.SubCommand != "" {
				fmt.Fprintf(os.Stderr, "Try '%s %s --help' for more information\n", config.Command.Name, argsErr.SubCommand)
			} else {
				fmt.Fprintf(os.Stderr, "Try '%s --help' for more information\n", config.Command.Name)
			}
			return nil, ErrShown
		}
		// For InvalidFlagError, print the error message with help hint
		var flagErr *InvalidFlagError
		if errors.As(err, &flagErr) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if flagErr.SubCommand != "" {
				fmt.Fprintf(os.Stderr, "Try '%s %s --help' for more information\n", config.Command.Name, flagErr.SubCommand)
			} else {
				fmt.Fprintf(os.Stderr, "Try '%s --help' for more information\n", config.Command.Name)
			}
			return nil, ErrShown
		}
		// For FlagValueError, check if verbose mode is enabled
		var flagValueErr *FlagValueError
		if errors.As(err, &flagValueErr) {
			// Check if verbose mode is enabled by examining the global flags.
			// We need to parse just the global flags to check the verbose setting.
			// We use the internal parse function which doesn't validate flag values.
			var globalFlags G
			var subCmdFlags S
			globalFlagNames := extractFlagNames(reflect.ValueOf(globalFlags))
			globalFlagTypes := extractFlagTypes(reflect.ValueOf(globalFlags))
			subCmdFlagTypes := extractFlagTypes(reflect.ValueOf(subCmdFlags))

			// Combine type maps for parsing
			allFlagTypes := make(map[string]reflect.Kind)
			for name, kind := range globalFlagTypes {
				allFlagTypes[name] = kind
			}
			for name, kind := range subCmdFlagTypes {
				allFlagTypes[name] = kind
			}

			// Parse without validation to extract flag values
			p, parseErr := parse(args, allFlagTypes, nil, true) // nil validFlags = no validation
			if parseErr == nil {
				// Categorize flags into GlobalFlags
				p.GlobalFlags = make(map[string]string)
				for name, value := range p.Flags {
					if globalFlagNames[name] {
						p.GlobalFlags[name] = value
					}
				}

				// Populate just the global flags struct, ignoring errors
				_ = populateStruct(reflect.ValueOf(&globalFlags).Elem(), p.GlobalFlags)
				// Always show user-friendly error message
				fmt.Fprintf(os.Stderr, "Error: %v\n", flagValueErr.UserMsg)
				// If verbose mode is enabled, also show the full error chain
				if isVerboseEnabled(globalFlags) {
					fmt.Fprintf(os.Stderr, "Error: %v\n", flagValueErr.Err)
				}
			} else {
				// If we can't even parse the flags, just show the normal error
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}

			if flagValueErr.SubCommand != "" {
				fmt.Fprintf(os.Stderr, "Try '%s %s --help' for more information\n", config.Command.Name, flagValueErr.SubCommand)
			} else {
				fmt.Fprintf(os.Stderr, "Try '%s --help' for more information\n", config.Command.Name)
			}
			return nil, ErrShown
		}
		return nil, err
	}
	return result, nil
}

// populateStruct populates a struct with values from a flag map.
func populateStruct(v reflect.Value, flags map[string]string) error {
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("value must be a struct")
	}

	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		fieldType := t.Field(i)

		// Skip unexported fields
		if !field.CanSet() {
			continue
		}

		flagName := fieldType.Tag.Get("flag")
		if flagName == "" {
			// Try using the field name as-is (or lowercase)
			flagName = strings.ToLower(fieldType.Name)
		}

		shortName := fieldType.Tag.Get("short")

		// Check if this flag exists in the parsed flags (try both long and short names)
		flagValue, ok := flags[flagName]
		if !ok && shortName != "" {
			flagValue, ok = flags[shortName]
		}
		if !ok {
			// Flag not set, check for default value
			defaultVal := fieldType.Tag.Get("default")
			if defaultVal != "" {
				flagValue = defaultVal
			} else {
				// No default, skip
				continue
			}
		}

		// Get port range tag if this is a Port type
		portRange := fieldType.Tag.Get("port")
		if err := setFieldValueWithPortRange(field, flagValue, portRange); err != nil {
			// Capture the user-friendly message before wrapping
			userMsg := err.Error()
			// Wrap the error with field context for the full error chain
			wrappedErr := fmt.Errorf("failed to set field %s: %w", fieldType.Name, err)
			// Return a FlagValueError with both user-friendly message and wrapped chain
			return &FlagValueError{
				FlagName:  flagName,
				FieldName: fieldType.Name,
				Value:     flagValue,
				UserMsg:   userMsg,
				Err:       wrappedErr,
			}
		}
	}

	return nil
}

// parsePortRange parses a port range string like "1-65535" or "8000-9000".
// Returns min, max, error. If the string is empty, returns 0, 0, nil (no validation).
func parsePortRange(rangeStr string) (min, max uint16, err error) {
	if rangeStr == "" {
		return 0, 0, nil
	}

	parts := strings.Split(rangeStr, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid port range format %q (expected \"min-max\")", rangeStr)
	}

	minVal, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid min port in range %q: %w", rangeStr, err)
	}

	maxVal, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid max port in range %q: %w", rangeStr, err)
	}

	if minVal > maxVal {
		return 0, 0, fmt.Errorf("invalid port range %q: min (%d) > max (%d)", rangeStr, minVal, maxVal)
	}

	return uint16(minVal), uint16(maxVal), nil
}

// parsePortValue parses a port value from string with user-friendly error messages.
func parsePortValue(value string) (Port, error) {
	portVal, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		if numErr, ok := err.(*strconv.NumError); ok && numErr.Err == strconv.ErrRange {
			return 0, fmt.Errorf("port must be between 0 and 65535, got %q", value)
		}
		return 0, fmt.Errorf("invalid port value %q", value)
	}
	return Port(portVal), nil
}

// setFieldValueWithPortRange sets a struct field value from a string flag value,
// with optional port range validation for Port types.
func setFieldValueWithPortRange(field reflect.Value, value string, portRange string) error {
	// For Port type, parse and validate the range
	if field.Type() == reflect.TypeOf(Port(0)) || (field.Kind() == reflect.Ptr && field.Type().Elem() == reflect.TypeOf(Port(0))) {
		// First try to parse the value to see what kind of error we get
		portVal, err := strconv.ParseUint(value, 10, 16)
		if err != nil {
			// Check if it's a range error and we have a port range specified
			if numErr, ok := err.(*strconv.NumError); ok && numErr.Err == strconv.ErrRange && portRange != "" {
				// Use the struct tag range in the error message instead of hardcoded 0-65535
				return fmt.Errorf("port must be between %s, got %q", portRange, value)
			}
			// For other parse errors, return the standard error
			if numErr, ok := err.(*strconv.NumError); ok && numErr.Err == strconv.ErrRange {
				return fmt.Errorf("port must be between 0 and 65535, got %q", value)
			}
			return fmt.Errorf("invalid port value %q", value)
		}

		port := Port(portVal)

		// Validate range if specified
		if portRange != "" {
			min, max, err := parsePortRange(portRange)
			if err != nil {
				return err
			}
			if port < Port(min) || port > Port(max) {
				return fmt.Errorf("port must be between %s, got %d", portRange, port)
			}
		}

		// Set the value
		if field.Kind() == reflect.Ptr {
			// Pointer to Port
			field.Set(reflect.ValueOf(&port))
			return nil
		}
		// Direct Port value
		field.Set(reflect.ValueOf(port))
		return nil
	}

	// For other types, use the original setFieldValue
	return setFieldValue(field, value)
}

// setFieldValue sets a struct field value from a string flag value.
func setFieldValue(field reflect.Value, value string) error {
	// Handle Port type (which is a uint16 alias)
	if field.Type() == reflect.TypeOf(Port(0)) {
		port, err := parsePortValue(value)
		if err != nil {
			return err
		}
		field.Set(reflect.ValueOf(port))
		return nil
	}

	switch field.Kind() {
	case reflect.Slice:
		if field.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported slice type %s", field.Type())
		}
		parts := strings.Split(value, ",")
		vals := make([]string, 0, len(parts))
		for _, part := range parts {
			if part == "" {
				continue
			}
			vals = append(vals, part)
		}
		slice := reflect.MakeSlice(field.Type(), len(vals), len(vals))
		for i, v := range vals {
			slice.Index(i).SetString(v)
		}
		field.Set(slice)
		return nil

	case reflect.String:
		field.SetString(value)
		return nil

	case reflect.Bool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool value %q: %w", value, err)
		}
		field.SetBool(b)
		return nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// Special case for time.Duration
		if field.Type() == reflect.TypeOf(time.Duration(0)) {
			d, err := time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("invalid duration %q: %w", value, err)
			}
			field.SetInt(int64(d))
			return nil
		}

		i, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid int value %q: %w", value, err)
		}
		field.SetInt(i)
		return nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid uint value %q: %w", value, err)
		}
		field.SetUint(u)
		return nil

	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float value %q: %w", value, err)
		}
		field.SetFloat(f)
		return nil

	case reflect.Ptr:
		// Handle pointer types - create a new value and set the pointer to it
		elemType := field.Type().Elem()

		// Handle *url.URL
		if field.Type() == reflect.TypeOf((*url.URL)(nil)) {
			u, err := url.Parse(value)
			if err != nil {
				return fmt.Errorf("invalid URL %q: %w", value, err)
			}
			field.Set(reflect.ValueOf(u))
			return nil
		}

		// Handle *Port
		if field.Type() == reflect.TypeOf((*Port)(nil)) {
			port, err := parsePortValue(value)
			if err != nil {
				return err
			}
			field.Set(reflect.ValueOf(&port))
			return nil
		}

		// Create a new value of the element type
		newValue := reflect.New(elemType)

		// Set the value using the same logic as non-pointer types
		if err := setFieldValue(newValue.Elem(), value); err != nil {
			return err
		}

		// Set the field to point to the new value
		field.Set(newValue)
		return nil

	case reflect.Struct:
		// Handle url.URL
		if field.Type() == reflect.TypeOf(url.URL{}) {
			u, err := url.Parse(value)
			if err != nil {
				return fmt.Errorf("invalid URL %q: %w", value, err)
			}
			field.Set(reflect.ValueOf(*u))
			return nil
		}
		return fmt.Errorf("unsupported struct type %s", field.Type())

	default:
		return fmt.Errorf("unsupported field type %s", field.Kind())
	}
}
