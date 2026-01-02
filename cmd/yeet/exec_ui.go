// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

type uiOverrides struct {
	ttyOverride *bool
	progress    catchrpc.ProgressMode
}

var execUIOverrides = uiOverrides{
	progress: catchrpc.ProgressAuto,
}

func resetExecUIOverrides() {
	execUIOverrides = uiOverrides{progress: catchrpc.ProgressAuto}
}

func applyGlobalUIFlags(flags globalFlagsParsed) error {
	resetExecUIOverrides()
	if flags.TTY && flags.NoTTY {
		return fmt.Errorf("cannot use --tty and --no-tty together")
	}
	if flags.TTY {
		execUIOverrides.ttyOverride = boolPtr(true)
	}
	if flags.NoTTY {
		execUIOverrides.ttyOverride = boolPtr(false)
	}
	if flags.Progress != "" {
		mode, err := parseProgressMode(flags.Progress)
		if err != nil {
			return err
		}
		execUIOverrides.progress = mode
	}
	return nil
}

func parseProgressMode(raw string) (catchrpc.ProgressMode, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "", string(catchrpc.ProgressAuto):
		return catchrpc.ProgressAuto, nil
	case string(catchrpc.ProgressTTY):
		return catchrpc.ProgressTTY, nil
	case string(catchrpc.ProgressPlain):
		return catchrpc.ProgressPlain, nil
	case string(catchrpc.ProgressQuiet):
		return catchrpc.ProgressQuiet, nil
	default:
		return "", fmt.Errorf("invalid progress mode %q (expected auto|tty|plain|quiet)", raw)
	}
}

func applyTTYOverride(tty bool) bool {
	if execUIOverrides.ttyOverride == nil {
		return tty
	}
	return *execUIOverrides.ttyOverride
}

func execProgressMode() catchrpc.ProgressMode {
	if execUIOverrides.progress == "" {
		return catchrpc.ProgressAuto
	}
	return execUIOverrides.progress
}

func boolPtr(v bool) *bool {
	return &v
}
