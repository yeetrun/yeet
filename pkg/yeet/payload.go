// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/yeetrun/yeet/pkg/codecutil"
	"github.com/yeetrun/yeet/pkg/ftdetect"
)

type namedReadCloser struct {
	io.ReadCloser
	name string
}

func (n *namedReadCloser) Name() string {
	return n.name
}

func openPayloadForUpload(file, goos, goarch string) (io.ReadCloser, func(), ftdetect.FileType, error) {
	ft, err := ftdetect.DetectFile(file, goos, goarch)
	if err != nil {
		return nil, nil, ftdetect.Unknown, fmt.Errorf("failed to detect file type: %w", err)
	}
	if ft != ftdetect.Binary {
		f, err := os.Open(file)
		if err != nil {
			return nil, nil, ft, err
		}
		return f, func() { f.Close() }, ft, nil
	}

	tmpPattern := fmt.Sprintf("yeet-zstd-%s-*.zst", filepath.Base(file))
	tmpFile, err := os.CreateTemp("", tmpPattern)
	if err != nil {
		return nil, nil, ft, err
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, nil, ft, err
	}
	if err := codecutil.ZstdCompress(file, tmpPath); err != nil {
		os.Remove(tmpPath)
		return nil, nil, ft, err
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return nil, nil, ft, err
	}
	payload := &namedReadCloser{ReadCloser: f, name: filepath.Base(file)}
	cleanup := func() {
		payload.Close()
		os.Remove(tmpPath)
	}
	return payload, cleanup, ft, nil
}
