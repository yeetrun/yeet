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
	fmt.Fprintf(w, "%s\n", b)
	return nil
}

func (e *ttyExecer) envCmdFunc(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("env requires a subcommand")
	}
	subcmd := args[0]
	args = args[1:]
	switch subcmd {
	case "show":
		flags, rest, err := cli.ParseEnvShow(args)
		if err != nil {
			return err
		}
		if len(rest) > 0 {
			return fmt.Errorf("env show takes no arguments")
		}
		sv, err := e.s.serviceView(e.sn)
		if err != nil {
			return err
		}
		return e.s.printEnv(e.rw, sv, flags.Staged)
	case "edit":
		if len(args) > 0 {
			return fmt.Errorf("env edit takes no arguments")
		}
		return e.editEnvCmdFunc()
	case "copy":
		if len(args) > 0 {
			return fmt.Errorf("env copy takes no arguments")
		}
		return e.envCopyCmdFunc()
	case "set":
		assignments, err := parseEnvAssignments(args)
		if err != nil {
			return err
		}
		return e.envSetCmdFunc(assignments)
	default:
		return fmt.Errorf("unknown env command %q", subcmd)
	}
}

func (e *ttyExecer) editEnvCmdFunc() error {
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return err
	}
	af := sv.AsStruct().Artifacts
	srcPath, _ := af.Latest(db.ArtifactEnvFile)
	tmpPath, err := copyToTmpFile(srcPath)
	if err != nil {
		return err
	}
	defer os.Remove(tmpPath)

	if err := e.editFile(tmpPath); err != nil {
		return fmt.Errorf("failed to edit env file: %w", err)
	}

	if srcPath != "" {
		if same, err := fileutil.Identical(srcPath, tmpPath); err != nil {
			return err
		} else if same {
			e.printf("No changes detected\n")
			return nil
		}
	} else {
		if st, err := os.Stat(tmpPath); err == nil && st.Size() == 0 {
			e.printf("No changes detected\n")
			return nil
		}
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to open temp file: %w", err)
	}
	defer f.Close()
	icfg := e.fileInstaller(netFlags{}, nil)
	icfg.EnvFile = true
	fi, err := NewFileInstaller(e.s, icfg)
	if err != nil {
		return fmt.Errorf("failed to create installer: %w", err)
	}
	defer fi.Close()
	if _, err := io.Copy(fi, f); err != nil {
		fi.Fail()
		return fmt.Errorf("failed to copy temp file to installer: %w", err)
	}
	return fi.Close()
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
	var contents []byte
	if ef != "" {
		contents, err = os.ReadFile(ef)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to read env file: %w", err)
		}
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
