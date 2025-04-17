// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package codecutil

import (
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

func ZstdCompress(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dstFile.Close()

	encoder, err := zstd.NewWriter(dstFile)
	if err != nil {
		return fmt.Errorf("failed to create zstd encoder: %w", err)
	}
	defer encoder.Close()

	_, err = io.Copy(encoder, srcFile)
	if err != nil {
		return fmt.Errorf("failed to compress file: %w", err)
	}

	return nil
}

func ZstdDecompress(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dstFile.Close()

	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return fmt.Errorf("failed to create zstd decoder: %w", err)
	}
	defer decoder.Close()

	err = decoder.Reset(srcFile)
	if err != nil {
		return fmt.Errorf("failed to reset decoder: %w", err)
	}

	_, err = decoder.WriteTo(dstFile)
	if err != nil {
		return fmt.Errorf("failed to decompress file: %w", err)
	}

	return nil
}
