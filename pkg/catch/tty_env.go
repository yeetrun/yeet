// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
)

func (s *Server) envFile(sv db.ServiceView, staged bool) (string, error) {
	af := sv.AsStruct().Artifacts
	if staged {
		ef, _ := af.Staged(db.ArtifactEnvFile)
		return ef, nil
	}
	ef, _ := af.Latest(db.ArtifactEnvFile)
	return ef, nil
}

func (s *Server) printEnv(w io.Writer, sv db.ServiceView, staged bool) error {
	ef, err := s.envFile(sv, staged)
	if err != nil {
		return err
	}
	if ef == "" {
		if staged {
			return fmt.Errorf("no staged env file found")
		}
		return fmt.Errorf("no env file found")
	}
	b, err := os.ReadFile(ef)
	if err != nil {
		return fmt.Errorf("failed to read env file: %w", err)
	}
	if _, err := fmt.Fprintf(w, "%s\n", b); err != nil {
		return fmt.Errorf("failed to write env file: %w", err)
	}
	return nil
}

type envSubcommand string

const (
	envSubcommandShow envSubcommand = "show"
	envSubcommandEdit envSubcommand = "edit"
	envSubcommandCopy envSubcommand = "copy"
	envSubcommandSet  envSubcommand = "set"
)

type envCommand struct {
	name        envSubcommand
	showFlags   cli.EnvShowFlags
	assignments []envAssignment
}

func envCommandFromArgs(args []string) (envCommand, error) {
	if len(args) == 0 {
		return envCommand{}, fmt.Errorf("env requires a subcommand")
	}
	subcmd := envSubcommand(args[0])
	args = args[1:]
	switch subcmd {
	case envSubcommandShow:
		return parseEnvShowCommand(args)
	case envSubcommandEdit, envSubcommandCopy:
		return parseEnvNoArgCommand(subcmd, args)
	case envSubcommandSet:
		return parseEnvSetCommand(args)
	default:
		return envCommand{}, fmt.Errorf("unknown env command %q", subcmd)
	}
}

func parseEnvShowCommand(args []string) (envCommand, error) {
	flags, rest, err := cli.ParseEnvShow(args)
	if err != nil {
		return envCommand{}, err
	}
	if len(rest) > 0 {
		return envCommand{}, fmt.Errorf("env show takes no arguments")
	}
	return envCommand{name: envSubcommandShow, showFlags: flags}, nil
}

func parseEnvNoArgCommand(subcmd envSubcommand, args []string) (envCommand, error) {
	if len(args) > 0 {
		return envCommand{}, fmt.Errorf("env %s takes no arguments", subcmd)
	}
	return envCommand{name: subcmd}, nil
}

func parseEnvSetCommand(args []string) (envCommand, error) {
	assignments, err := parseEnvAssignments(args)
	if err != nil {
		return envCommand{}, err
	}
	return envCommand{name: envSubcommandSet, assignments: assignments}, nil
}

func (e *ttyExecer) envCmdFunc(args []string) error {
	cmd, err := envCommandFromArgs(args)
	if err != nil {
		return err
	}
	return e.runEnvCommand(cmd)
}

func (e *ttyExecer) runEnvCommand(cmd envCommand) error {
	switch cmd.name {
	case envSubcommandShow:
		sv, err := e.s.serviceView(e.sn)
		if err != nil {
			return err
		}
		return e.s.printEnv(e.rw, sv, cmd.showFlags.Staged)
	case envSubcommandEdit:
		return e.editEnvCmdFunc()
	case envSubcommandCopy:
		return e.envCopyCmdFunc()
	case envSubcommandSet:
		return e.envSetCmdFunc(cmd.assignments)
	default:
		return fmt.Errorf("unknown env command %q", cmd.name)
	}
}

func (e *ttyExecer) editEnvCmdFunc() (retErr error) {
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return err
	}
	srcPath := latestEnvFilePath(sv)
	tmpPath, err := copyToTmpFile(srcPath)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, removeTempFile(tmpPath))
	}()

	if err := e.editFile(tmpPath); err != nil {
		return fmt.Errorf("failed to edit env file: %w", err)
	}

	changed, err := editedEnvFileChanged(srcPath, tmpPath)
	if err != nil {
		return err
	}
	if !changed {
		e.printf("No changes detected\n")
		return nil
	}

	return e.installEditedEnvFile(tmpPath)
}

func latestEnvFilePath(sv db.ServiceView) string {
	af := sv.AsStruct().Artifacts
	srcPath, _ := af.Latest(db.ArtifactEnvFile)
	return srcPath
}

func editedEnvFileChanged(srcPath, tmpPath string) (bool, error) {
	if srcPath == "" {
		st, err := os.Stat(tmpPath)
		if err != nil {
			return false, fmt.Errorf("failed to stat temp file: %w", err)
		}
		return st.Size() != 0, nil
	}
	same, err := fileutil.Identical(srcPath, tmpPath)
	if err != nil {
		return false, err
	}
	return !same, nil
}

func removeTempFile(path string) error {
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("failed to remove temp file: %w", err)
	}
	return nil
}

func closeEnvFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	return nil
}

func (e *ttyExecer) installEditedEnvFile(tmpPath string) (retErr error) {
	f, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to open temp file: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, closeEnvFile(f))
	}()
	icfg := e.fileInstaller(netFlags{}, nil)
	icfg.EnvFile = true
	fi, err := NewFileInstaller(e.s, icfg)
	if err != nil {
		return fmt.Errorf("failed to create installer: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, closeEnvInstaller(fi))
	}()
	if _, err := io.Copy(fi, f); err != nil {
		fi.Fail()
		return fmt.Errorf("failed to copy temp file to installer: %w", err)
	}
	return fi.Close()
}

func closeEnvInstaller(fi *FileInstaller) error {
	if err := fi.Close(); err != nil {
		return fmt.Errorf("failed to close env installer: %w", err)
	}
	return nil
}

func (e *ttyExecer) envCopyCmdFunc() error {
	cfg := e.fileInstaller(netFlags{}, nil)
	cfg.EnvFile = true
	if sv, err := e.s.serviceView(e.sn); err != nil {
		if errors.Is(err, errServiceNotFound) {
			cfg.StageOnly = true
		} else {
			return err
		}
	} else if sv.ServiceType() == "" {
		cfg.StageOnly = true
	}
	return e.install("env", e.payloadReader(), cfg)
}

type envAssignment struct {
	Key   string
	Value string
}

var (
	envKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	envLineRe = regexp.MustCompile(`^(\s*(?:export\s+)?)([A-Za-z_][A-Za-z0-9_]*)\s*=`)
)

func parseEnvAssignments(args []string) ([]envAssignment, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("env set requires at least one KEY=VALUE assignment")
	}
	seen := make(map[string]int, len(args))
	assignments := make([]envAssignment, 0, len(args))
	for _, arg := range args {
		key, value, err := splitEnvAssignment(arg)
		if err != nil {
			return nil, err
		}
		if idx, ok := seen[key]; ok {
			assignments[idx].Value = value
			continue
		}
		seen[key] = len(assignments)
		assignments = append(assignments, envAssignment{Key: key, Value: value})
	}
	return assignments, nil
}

func splitEnvAssignment(arg string) (string, string, error) {
	i := strings.Index(arg, "=")
	if i <= 0 {
		return "", "", fmt.Errorf("invalid env assignment %q (expected KEY=VALUE)", arg)
	}
	key := arg[:i]
	value := arg[i+1:]
	if strings.TrimSpace(key) != key {
		return "", "", fmt.Errorf("invalid env key %q (contains whitespace)", key)
	}
	if !isValidEnvKey(key) {
		return "", "", fmt.Errorf("invalid env key %q", key)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", "", fmt.Errorf("invalid env value for %q (contains a line break or NUL byte)", key)
	}
	return key, value, nil
}

func isValidEnvKey(key string) bool {
	return envKeyRe.MatchString(key)
}

type envUpdates struct {
	values map[string]string
	order  []string
}

func newEnvUpdates(assignments []envAssignment) envUpdates {
	updates := envUpdates{
		values: make(map[string]string, len(assignments)),
		order:  make([]string, 0, len(assignments)),
	}
	for _, a := range assignments {
		if _, ok := updates.values[a.Key]; !ok {
			updates.order = append(updates.order, a.Key)
		}
		updates.values[a.Key] = a.Value
	}
	return updates
}

func splitEnvFileLines(contents []byte) ([]string, bool) {
	raw := string(contents)
	hadTrailingNewline := strings.HasSuffix(raw, "\n")
	raw = strings.TrimSuffix(raw, "\n")
	if raw == "" {
		return nil, hadTrailingNewline
	}
	return strings.Split(raw, "\n"), hadTrailingNewline
}

type parsedEnvLine struct {
	prefix string
	key    string
}

func parseEnvLine(line string) (parsedEnvLine, bool) {
	matches := envLineRe.FindStringSubmatch(line)
	if len(matches) == 0 {
		return parsedEnvLine{}, false
	}
	return parsedEnvLine{prefix: matches[1], key: matches[2]}, true
}

func rewriteEnvLines(lines []string, updates envUpdates) ([]string, map[string]bool, bool) {
	updated := make(map[string]bool, len(updates.values))
	changed := false
	for i := 0; i < len(lines); {
		parsed, ok := parseEnvLine(lines[i])
		if !ok {
			i++
			continue
		}
		val, ok := updates.values[parsed.key]
		if !ok {
			i++
			continue
		}
		updated[parsed.key] = true
		if val == "" {
			lines = append(lines[:i], lines[i+1:]...)
			changed = true
			continue
		}
		newLine := parsed.prefix + parsed.key + "=" + val
		if newLine != lines[i] {
			lines[i] = newLine
			changed = true
		}
		i++
	}
	return lines, updated, changed
}

func appendMissingEnvLines(lines []string, updates envUpdates, updated map[string]bool) ([]string, bool) {
	changed := false
	for _, key := range updates.order {
		if updated[key] {
			continue
		}
		val := updates.values[key]
		if val == "" {
			continue
		}
		lines = append(lines, key+"="+val)
		changed = true
	}
	return lines, changed
}

func joinEnvFileLines(lines []string, hadTrailingNewline bool) []byte {
	out := strings.Join(lines, "\n")
	if out != "" || hadTrailingNewline || len(lines) > 0 {
		out += "\n"
	}
	return []byte(out)
}

func applyEnvAssignments(contents []byte, assignments []envAssignment) ([]byte, bool, error) {
	if len(assignments) == 0 {
		return contents, false, fmt.Errorf("no env assignments provided")
	}
	lines, hadTrailingNewline := splitEnvFileLines(contents)
	updates := newEnvUpdates(assignments)

	var changed bool
	lines, updated, changed := rewriteEnvLines(lines, updates)
	if appendedLines, appended := appendMissingEnvLines(lines, updates, updated); appended {
		lines = appendedLines
		changed = true
	}
	if !changed {
		return contents, false, nil
	}
	return joinEnvFileLines(lines, hadTrailingNewline), true, nil
}

func (e *ttyExecer) envSetCmdFunc(assignments []envAssignment) error {
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return err
	}
	ef, err := e.s.envFile(sv, false)
	if err != nil {
		return err
	}
	contents, err := readEnvFileIfExists(ef)
	if err != nil {
		return err
	}
	updated, changed, err := applyEnvAssignments(contents, assignments)
	if err != nil {
		return err
	}
	if !changed {
		e.printf("No changes detected\n")
		return nil
	}
	cfg := e.fileInstaller(netFlags{}, nil)
	cfg.EnvFile = true
	return e.install("env", bytes.NewReader(updated), cfg)
}

func readEnvFileIfExists(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	contents, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read env file: %w", err)
	}
	return contents, nil
}
