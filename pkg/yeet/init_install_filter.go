// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

type initInstallFilter struct {
	out     io.Writer
	buf     bytes.Buffer
	summary initInstallSummary
}

func newInitInstallFilter(out io.Writer) *initInstallFilter {
	return &initInstallFilter{out: out}
}

func (f *initInstallFilter) Write(p []byte) (int, error) {
	written := len(p)
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx == -1 {
			if _, err := f.buf.Write(p); err != nil {
				return written, err
			}
			if shouldFlushPartial(f.buf.String()) {
				if _, err := f.out.Write(f.buf.Bytes()); err != nil {
					return written, err
				}
				f.buf.Reset()
			}
			return written, nil
		}
		if _, err := f.buf.Write(p[:idx]); err != nil {
			return written, err
		}
		line := f.buf.String()
		f.buf.Reset()
		if err := f.handleLine(line); err != nil {
			return written, err
		}
		p = p[idx+1:]
	}
	return written, nil
}

func (f *initInstallFilter) SummaryDetail() string {
	return f.summary.Detail()
}

func (f *initInstallFilter) WarningSummary() string {
	return f.summary.WarningSummary()
}

func (f *initInstallFilter) InfoSummary() string {
	return f.summary.InfoSummary()
}

func (f *initInstallFilter) handleLine(line string) error {
	msg := strings.TrimSpace(stripLogPrefix(line))
	if msg == "" {
		return nil
	}
	if f.summary.Absorb(msg) {
		return nil
	}
	if isImportantInitLine(msg) {
		return f.writeLine(msg)
	}
	return nil
}

func (f *initInstallFilter) writeLine(msg string) error {
	_, err := fmt.Fprintln(f.out, msg)
	return err
}

type initInstallSummary struct {
	dataDir       string
	tsnetRoot     string
	skippedImages int
	warnings      []string
	infos         []string
}

func (s *initInstallSummary) Absorb(msg string) bool {
	if s.absorbPath(msg) {
		return true
	}
	if s.absorbImageLine(msg) {
		return true
	}
	if isIgnoredInstallProgressLine(msg) {
		return true
	}
	if isInstallWarningLine(msg) {
		s.addWarning(msg)
		return true
	}
	if isVisibleWarningLine(msg) {
		s.addWarning(msg)
		return false
	}
	return false
}

func (s *initInstallSummary) absorbPath(msg string) bool {
	switch {
	case strings.HasPrefix(msg, "data dir:"):
		s.dataDir = strings.TrimSpace(strings.TrimPrefix(msg, "data dir:"))
		return true
	case strings.HasPrefix(msg, "tsnet running state path"):
		path := strings.TrimSpace(strings.TrimPrefix(msg, "tsnet running state path"))
		if path != "" {
			s.tsnetRoot = filepath.Dir(path)
		}
		return true
	case strings.HasPrefix(msg, "tsnet starting with hostname"):
		if root := extractQuotedValue(msg, "varRoot"); root != "" {
			s.tsnetRoot = root
		}
		return true
	default:
		return false
	}
}

func (s *initInstallSummary) absorbImageLine(msg string) bool {
	switch {
	case strings.HasPrefix(msg, "skipping image "):
		s.skippedImages++
		return true
	case strings.HasPrefix(msg, "image ") && strings.Contains(msg, " not found"):
		return true
	case strings.HasPrefix(msg, "setting image "):
		return true
	default:
		return false
	}
}

func isIgnoredInstallProgressLine(msg string) bool {
	prefixes := []string{
		"NetNS:",
		"Requires:",
		"Detected ",
		"File moved to ",
		"Removed old file:",
		"copying ",
		"adding unit ",
		"Installing service",
		"File received",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(msg, prefix) {
			return true
		}
	}
	if strings.HasPrefix(msg, "Service \"") && strings.HasSuffix(msg, "\" installed") {
		return true
	}
	return strings.HasPrefix(msg, "no ") && strings.Contains(msg, "artifact")
}

func isInstallWarningLine(msg string) bool {
	return strings.HasPrefix(msg, "Installation of ")
}

func isVisibleWarningLine(msg string) bool {
	return strings.HasPrefix(msg, "Failed to install service:") || strings.HasPrefix(msg, "Warning:")
}

func (s *initInstallSummary) Detail() string {
	parts := []string{"systemd"}
	if s.dataDir != "" {
		parts = append(parts, "data="+s.dataDir)
	}
	if s.tsnetRoot != "" {
		parts = append(parts, "tsnet="+s.tsnetRoot)
	}
	if s.skippedImages > 0 {
		parts = append(parts, fmt.Sprintf("skipped-images=%d", s.skippedImages))
	}
	return strings.Join(parts, " ")
}

func (s *initInstallSummary) WarningSummary() string {
	return strings.Join(uniqueStrings(s.warnings), "; ")
}

func (s *initInstallSummary) InfoSummary() string {
	return strings.Join(uniqueStrings(s.infos), "; ")
}

func (s *initInstallSummary) addWarning(msg string) {
	s.warnings = append(s.warnings, msg)
}

func stripLogPrefix(line string) string {
	if len(line) < 20 {
		return line
	}
	if line[4] == '/' && line[7] == '/' && line[10] == ' ' &&
		line[13] == ':' && line[16] == ':' && line[19] == ' ' {
		return line[20:]
	}
	return line
}

func extractQuotedValue(line, key string) string {
	needle := key + " \""
	idx := strings.Index(line, needle)
	if idx == -1 {
		return ""
	}
	start := idx + len(needle)
	end := strings.Index(line[start:], "\"")
	if end == -1 {
		return ""
	}
	return line[start : start+end]
}

func isImportantInitLine(msg string) bool {
	if strings.HasPrefix(msg, "Warning:") || strings.HasPrefix(msg, "Error:") {
		return true
	}
	if strings.Contains(strings.ToLower(msg), "failed") || strings.Contains(strings.ToLower(msg), "error") {
		return true
	}
	return false
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func shouldFlushPartial(buf string) bool {
	trimmed := strings.TrimSpace(buf)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "password") {
		return true
	}
	if strings.Contains(trimmed, "[y/N]") || strings.Contains(trimmed, "[y/n]") {
		return true
	}
	if strings.HasSuffix(trimmed, ":") || strings.HasSuffix(trimmed, ": ") {
		return true
	}
	return false
}
