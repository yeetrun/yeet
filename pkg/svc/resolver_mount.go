// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ResolverMountProbe identifies a resolver source, target, and mountinfo view.
type ResolverMountProbe struct {
	SourcePath    string
	TargetPath    string
	MountInfoPath string
	MountPoint    string
}

// VerifyReadOnlyResolverMount reports whether the resolver target is the
// expected source file and its deepest covering mount is read-only.
func VerifyReadOnlyResolverMount(probe ResolverMountProbe) error {
	return verifyReadOnlyResolverMount(probe, resolverMountProbeDeps{
		stat: os.Stat,
		open: func(path string) (io.ReadCloser, error) {
			return os.Open(path)
		},
	})
}

type resolverMountEntry struct {
	ID         int
	MountPoint string
	ReadOnly   bool
}

type resolverMountProbeDeps struct {
	stat func(string) (os.FileInfo, error)
	open func(string) (io.ReadCloser, error)
}

func verifyReadOnlyResolverMount(probe ResolverMountProbe, deps resolverMountProbeDeps) error {
	if err := validateResolverMountProbePaths(probe); err != nil {
		return err
	}
	if err := verifyResolverMountFileIdentity(probe, deps.stat); err != nil {
		return err
	}
	return verifyResolverMountInfo(probe, deps.open)
}

func validateResolverMountProbePaths(probe ResolverMountProbe) error {
	for _, path := range []struct {
		name  string
		value string
	}{
		{name: "resolver source path", value: probe.SourcePath},
		{name: "resolver target path", value: probe.TargetPath},
		{name: "mountinfo path", value: probe.MountInfoPath},
		{name: "resolver mount point", value: probe.MountPoint},
	} {
		if !isCleanAbsoluteMountInfoPath(path.value) {
			return fmt.Errorf("%s must be a clean absolute path", path.name)
		}
	}
	return nil
}

func verifyResolverMountFileIdentity(
	probe ResolverMountProbe,
	stat func(string) (os.FileInfo, error),
) error {
	sourceInfo, err := resolverMountRegularFileInfo("source", probe.SourcePath, stat)
	if err != nil {
		return err
	}
	targetInfo, err := resolverMountRegularFileInfo("target", probe.TargetPath, stat)
	if err != nil {
		return err
	}
	if !os.SameFile(sourceInfo, targetInfo) {
		return fmt.Errorf("resolver source and target do not refer to the same file")
	}
	return nil
}

func resolverMountRegularFileInfo(
	kind string,
	path string,
	stat func(string) (os.FileInfo, error),
) (os.FileInfo, error) {
	info, err := stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat resolver %s %s: %w", kind, path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("resolver %s %s is not a regular file", kind, path)
	}
	return info, nil
}

func verifyResolverMountInfo(
	probe ResolverMountProbe,
	open func(string) (io.ReadCloser, error),
) error {
	mountInfo, err := open(probe.MountInfoPath)
	if err != nil {
		return fmt.Errorf("open mountinfo %s: %w", probe.MountInfoPath, err)
	}

	entry, err := visibleResolverMount(mountInfo, probe.MountPoint)
	if err = closeResolverMountInfo(probe.MountInfoPath, mountInfo, err); err != nil {
		return err
	}
	if !entry.ReadOnly {
		return fmt.Errorf("resolver mount point %s is not read-only", probe.MountPoint)
	}
	return nil
}

func closeResolverMountInfo(path string, mountInfo io.Closer, prior error) error {
	if err := mountInfo.Close(); err != nil {
		return errors.Join(prior, fmt.Errorf("close mountinfo %s: %w", path, err))
	}
	return prior
}

func visibleResolverMount(r io.Reader, mountPoint string) (resolverMountEntry, error) {
	scanner := bufio.NewScanner(r)
	var visible resolverMountEntry
	line := 0
	for scanner.Scan() {
		line++
		entry, err := parseResolverMountRecord(scanner.Text(), line, mountPoint)
		if err != nil {
			return resolverMountEntry{}, err
		}
		if mountInfoPathCovers(entry.MountPoint, mountPoint) &&
			(len(entry.MountPoint) > len(visible.MountPoint) ||
				(len(entry.MountPoint) == len(visible.MountPoint) && entry.ID > visible.ID)) {
			visible = entry
		}
	}
	if err := scanner.Err(); err != nil {
		return resolverMountEntry{}, fmt.Errorf("read mountinfo: %w", err)
	}
	if visible.ID == 0 {
		return resolverMountEntry{}, fmt.Errorf("resolver mount point %s is absent from mountinfo", mountPoint)
	}
	return visible, nil
}

func parseResolverMountRecord(raw string, line int, mountPoint string) (resolverMountEntry, error) {
	fields, separator, err := resolverMountRecordFields(raw, line)
	if err != nil {
		return resolverMountEntry{}, err
	}
	id, err := resolverMountRecordID(fields, line)
	if err != nil {
		return resolverMountEntry{}, err
	}
	decodedMountPoint, err := resolverMountRecordPaths(fields, mountPoint)
	if err != nil {
		return resolverMountEntry{}, err
	}
	readOnly, err := resolverMountRecordReadOnly(fields, separator, line)
	if err != nil {
		return resolverMountEntry{}, err
	}
	return resolverMountEntry{ID: id, MountPoint: decodedMountPoint, ReadOnly: readOnly}, nil
}

func resolverMountRecordFields(raw string, line int) ([]string, int, error) {
	fields := strings.Fields(raw)
	if len(fields) < 6 {
		return nil, 0, fmt.Errorf("mountinfo record %d: expected at least 10 fields", line)
	}
	separator := mountInfoSeparator(fields)
	if separator == -1 && len(fields) < 10 {
		return nil, 0, fmt.Errorf("mountinfo record %d: expected at least 10 fields", line)
	}
	if separator == -1 {
		return nil, 0, fmt.Errorf("mountinfo record %d: missing separator", line)
	}
	if len(fields) != separator+4 {
		return nil, 0, fmt.Errorf("mountinfo record %d: expected three fields after separator", line)
	}
	return fields, separator, nil
}

func mountInfoSeparator(fields []string) int {
	for i := 6; i < len(fields); i++ {
		if fields[i] == "-" {
			return i
		}
	}
	return -1
}

func resolverMountRecordID(fields []string, line int) (int, error) {
	id, ok := parseMountInfoID(fields[0])
	if !ok || id <= 0 {
		return 0, fmt.Errorf("mountinfo record %d: invalid mount ID %q", line, fields[0])
	}
	if _, ok := parseMountInfoID(fields[1]); !ok {
		return 0, fmt.Errorf("mountinfo record %d: invalid parent mount ID %q", line, fields[1])
	}
	if !validMountInfoDevice(fields[2]) {
		return 0, fmt.Errorf("mountinfo record %d: invalid major:minor device %q", line, fields[2])
	}
	return id, nil
}

func resolverMountRecordPaths(fields []string, mountPoint string) (string, error) {
	decodedRoot, err := unescapeMountInfoPath(fields[3])
	if err != nil {
		return "", fmt.Errorf("decode mount root %q: %w", fields[3], err)
	}
	decodedMountPoint, err := unescapeMountInfoPath(fields[4])
	if err != nil {
		return "", fmt.Errorf("decode mount point %q: %w", fields[4], err)
	}
	if !isCleanAbsoluteMountInfoPath(decodedMountPoint) {
		return "", fmt.Errorf("mount point %q must be a clean absolute path", decodedMountPoint)
	}
	if mountInfoPathCovers(decodedMountPoint, mountPoint) &&
		!isCleanAbsoluteMountInfoPath(decodedRoot) {
		return "", fmt.Errorf("mount root %q must be a clean absolute path", decodedRoot)
	}
	return decodedMountPoint, nil
}

func mountInfoPathCovers(mountPoint, path string) bool {
	if mountPoint == "/" {
		return true
	}
	return path == mountPoint || strings.HasPrefix(path, mountPoint+"/")
}

func resolverMountRecordReadOnly(fields []string, separator, line int) (bool, error) {
	readOnly, validOptions := readOnlyMountInfoOptions(fields[5])
	if !validOptions {
		return false, fmt.Errorf("mountinfo record %d: invalid per-mount options %q", line, fields[5])
	}
	if fields[separator+1] == "-" {
		return false, fmt.Errorf("mountinfo record %d: invalid filesystem type", line)
	}
	if fields[separator+2] == "-" {
		return false, fmt.Errorf("mountinfo record %d: invalid mount source", line)
	}
	if !validMountInfoOptionList(fields[separator+3]) {
		return false, fmt.Errorf("mountinfo record %d: invalid super options %q", line, fields[separator+3])
	}
	return readOnly, nil
}

func parseMountInfoID(raw string) (int, bool) {
	if !isDecimal(raw) {
		return 0, false
	}
	id, err := strconv.ParseUint(raw, 10, strconv.IntSize)
	if err != nil {
		return 0, false
	}
	return int(id), true
}

func validMountInfoDevice(raw string) bool {
	major, minor, ok := strings.Cut(raw, ":")
	return ok && isDecimal(major) && isDecimal(minor)
}

func isDecimal(raw string) bool {
	if raw == "" {
		return false
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isCleanAbsoluteMountInfoPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func readOnlyMountInfoOptions(options string) (bool, bool) {
	if !validMountInfoOptionList(options) {
		return false, false
	}
	var readOnly, accessModeSet bool
	for _, option := range strings.Split(options, ",") {
		switch option {
		case "ro":
			if accessModeSet {
				return false, false
			}
			readOnly = true
			accessModeSet = true
		case "rw":
			if accessModeSet {
				return false, false
			}
			accessModeSet = true
		}
	}
	return readOnly, accessModeSet
}

func validMountInfoOptionList(options string) bool {
	if options == "" || options == "-" {
		return false
	}
	for _, option := range strings.Split(options, ",") {
		if option == "" {
			return false
		}
	}
	return true
}

func unescapeMountInfoPath(raw string) (string, error) {
	var decoded strings.Builder
	for len(raw) > 0 {
		if raw[0] != '\\' {
			decoded.WriteByte(raw[0])
			raw = raw[1:]
			continue
		}
		if len(raw) < 4 {
			return "", fmt.Errorf("invalid mountinfo escape %q", raw)
		}
		escape := raw[:4]
		switch escape {
		case `\040`:
			decoded.WriteByte(' ')
		case `\011`:
			decoded.WriteByte('\t')
		case `\012`:
			decoded.WriteByte('\n')
		case `\134`:
			decoded.WriteByte('\\')
		default:
			return "", fmt.Errorf("invalid mountinfo escape %q", escape)
		}
		raw = raw[4:]
	}
	return decoded.String(), nil
}
