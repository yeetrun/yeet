// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
)

const vmRuntimeImportStdinPath = "-"

type vmRuntimeImportEntry struct {
	name string
	mode int64
}

var vmRuntimeImportEntries = []vmRuntimeImportEntry{
	{name: "runtime-manifest.json", mode: 0o644},
	{name: "firecracker", mode: 0o755},
	{name: "jailer", mode: 0o755},
}

type vmRuntimeImportFile struct {
	name string
	mode int64
	size int64
	file *os.File
}

func handleVMRuntimeImportParsed(ctx context.Context, _ cli.VMRuntimeFlags, remaining []string) error {
	if len(remaining) != 3 || remaining[0] != cli.VMRuntimeActionImport {
		return fmt.Errorf("vm runtime import requires a name and directory")
	}
	files, err := openVMRuntimeImportFiles(remaining[2])
	if err != nil {
		return err
	}

	pr, pw := io.Pipe()
	producerDone := make(chan error, 1)
	go func() {
		err := writeVMRuntimeImportTar(pw, files)
		closeVMRuntimeImportFiles(files)
		_ = pw.CloseWithError(err)
		producerDone <- err
	}()

	remoteArgs := []string{"vm", "runtime", cli.VMRuntimeActionImport, remaining[1], vmRuntimeImportStdinPath}
	remoteErr := withRemoteExecTTYDisabled(func() error {
		return execRemoteFn(ctx, systemServiceName, remoteArgs, pr, false)
	})
	_ = pr.CloseWithError(remoteErr)
	producerErr := <-producerDone
	if remoteErr != nil {
		return remoteErr
	}
	if producerErr != nil {
		return fmt.Errorf("stream VM runtime import: %w", producerErr)
	}
	return nil
}

func openVMRuntimeImportFiles(dir string) ([]vmRuntimeImportFile, error) {
	dirInfo, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("VM runtime directory does not exist: %s", dir)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect VM runtime directory: %w", err)
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return nil, fmt.Errorf("VM runtime path must be a directory and not a symlink: %s", dir)
	}

	files := make([]vmRuntimeImportFile, 0, len(vmRuntimeImportEntries))
	for _, entry := range vmRuntimeImportEntries {
		opened, err := openVMRuntimeImportFile(dir, entry)
		if err == nil {
			files = append(files, opened)
			continue
		}
		closeVMRuntimeImportFiles(files)
		return nil, err
	}
	return files, nil
}

func openVMRuntimeImportFile(dir string, entry vmRuntimeImportEntry) (vmRuntimeImportFile, error) {
	path := filepath.Join(dir, entry.name)
	lstatInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return vmRuntimeImportFile{}, fmt.Errorf("VM runtime is missing required file %s", entry.name)
	}
	if err != nil {
		return vmRuntimeImportFile{}, fmt.Errorf("inspect VM runtime file %s: %w", entry.name, err)
	}
	if lstatInfo.Mode()&os.ModeSymlink != 0 || !lstatInfo.Mode().IsRegular() {
		return vmRuntimeImportFile{}, fmt.Errorf("VM runtime file %s must be a regular file, not a symlink", entry.name)
	}
	file, err := os.Open(path)
	if err != nil {
		return vmRuntimeImportFile{}, fmt.Errorf("open VM runtime file %s: %w", entry.name, err)
	}
	openedInfo, statErr := file.Stat()
	if statErr == nil && openedInfo.Mode().IsRegular() && os.SameFile(lstatInfo, openedInfo) {
		return vmRuntimeImportFile{name: entry.name, mode: entry.mode, size: openedInfo.Size(), file: file}, nil
	}
	_ = file.Close()
	if statErr != nil {
		return vmRuntimeImportFile{}, fmt.Errorf("inspect opened VM runtime file %s: %w", entry.name, statErr)
	}
	return vmRuntimeImportFile{}, fmt.Errorf("VM runtime file %s changed while opening", entry.name)
}

func closeVMRuntimeImportFiles(files []vmRuntimeImportFile) {
	for _, entry := range files {
		_ = entry.file.Close()
	}
}

func writeVMRuntimeImportTar(w io.Writer, files []vmRuntimeImportFile) error {
	tw := tar.NewWriter(w)
	for _, entry := range files {
		header := &tar.Header{
			Name:     entry.name,
			Mode:     entry.mode,
			Size:     entry.size,
			ModTime:  time.Unix(0, 0).UTC(),
			Typeflag: tar.TypeReg,
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(header); err != nil {
			_ = tw.Close()
			return err
		}
		if _, err := io.Copy(tw, entry.file); err != nil {
			_ = tw.Close()
			return err
		}
	}
	return tw.Close()
}
