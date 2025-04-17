// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package targz

import (
	"archive/tar"
	"compress/gzip"
	"io"
)

type Reader struct {
	z *gzip.Reader
	r *tar.Reader
}

func (r Reader) Read(p []byte) (n int, err error) {
	return r.r.Read(p)
}

func (r Reader) Close() error {
	return r.z.Close()
}

func (r Reader) Next() (*tar.Header, error) {
	return r.r.Next()
}

func New(r io.Reader) (*Reader, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	return &Reader{z: gz, r: tar.NewReader(gz)}, nil
}

// ReadFile calls f for each entry in the tarball.
func ReadFile(r io.Reader, f func(*tar.Header, io.Reader) error) error {
	t, err := New(r)
	if err != nil {
		return err
	}
	defer t.Close()

	for {
		header, err := t.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := f(header, t); err != nil {
			return err
		}
	}
	return nil
}
