// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"io"

	"github.com/yeetrun/yeet/pkg/tui"
)

type fdWriter interface {
	Fd() uintptr
}

func outputStyles(w io.Writer) tui.Styles {
	fd, ok := w.(fdWriter)
	return tui.NewStyles(ok && isTerminalFn(int(fd.Fd())))
}
