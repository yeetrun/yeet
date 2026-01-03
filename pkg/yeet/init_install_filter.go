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
			f.buf.Write(p)
			if shouldFlushPartial(f.buf.String()) {
				if _, err := f.out.Write(f.buf.Bytes()); err != nil {
					return written, err
				}
				f.buf.Reset()
			}
			return written, nil
		}
		f.buf.Write(p[:idx])
		line := f.buf.String()
		f.buf.Reset()
		f.handleLine(line)
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

func (f *initInstallFilter) handleLine(line string) {
	msg := strings.TrimSpace(stripLogPrefix(line))
	if msg == "" {
		return
	}
	if f.summary.Absorb(msg) {
		return
	}
	if isImportantInitLine(msg) {
		f.writeLine(msg)
	}
}

func (f *initInstallFilter) writeLine(msg string) {
	fmt.Fprintln(f.out, msg)
}

type initInstallSummary struct {
	dataDir       string
	tsnetRoot     string
	skippedImages int
	warnings      []string
	infos         []string
}

func (s *initInstallSummary) Absorb(msg string) bool {
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
	case strings.HasPrefix(msg, "NetNS:"):
		return true
	case strings.HasPrefix(msg, "Requires:"):
		return true
	case strings.HasPrefix(msg, "skipping image "):
		s.skippedImages++
		return true
	case strings.HasPrefix(msg, "image ") && strings.Contains(msg, " not found"):
		return true
	case strings.HasPrefix(msg, "setting image "):
		return true
	case strings.HasPrefix(msg, "Detected "):
		return true
	case strings.HasPrefix(msg, "File moved to "):
		return true
	case strings.HasPrefix(msg, "Removed old file:"):
		return true
	case strings.HasPrefix(msg, "copying "):
		return true
	case strings.HasPrefix(msg, "adding unit "):
		return true
	case strings.HasPrefix(msg, "no ") && strings.Contains(msg, "artifact"):
		return true
	case strings.HasPrefix(msg, "Installing service"):
		return true
	case strings.HasPrefix(msg, "Service \"") && strings.HasSuffix(msg, "\" installed"):
		return true
	case strings.HasPrefix(msg, "File received"):
		return true
	case strings.HasPrefix(msg, "Installation of "):
		s.addWarning(msg)
		return true
	case strings.HasPrefix(msg, "Failed to install service:"):
		s.addWarning(msg)
		return false
	case strings.HasPrefix(msg, "Warning:"):
		s.addWarning(msg)
		return false
	}
	return false
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
