// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmdutil

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func NewStdCmd(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func Confirm(r io.Reader, w io.Writer, msg string) (bool, error) {
	fmt.Fprintf(w, "%s [y/N]: ", msg)

	var confirm string
	_, err := fmt.Fscanln(r, &confirm)
	if err != nil && err.Error() != "unexpected newline" {
		return false, fmt.Errorf("failed to read confirmation: %w", err)
	}
	if strings.ToLower(confirm) != "y" {
		return false, nil
	}
	return true, nil
}
