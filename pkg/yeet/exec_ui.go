// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"fmt"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

type UIConfig struct {
	TTYOverride *bool
	Progress    catchrpc.ProgressMode
}

var execUIOverrides = UIConfig{
	Progress: catchrpc.ProgressAuto,
}

func ResetUIConfig() {
	execUIOverrides = UIConfig{Progress: catchrpc.ProgressAuto}
}

func SetUIConfig(cfg UIConfig) {
	execUIOverrides = cfg
	if execUIOverrides.Progress == "" {
		execUIOverrides.Progress = catchrpc.ProgressAuto
	}
}

func CurrentUIConfig() UIConfig {
	return execUIOverrides
}

func ParseProgressMode(raw string) (catchrpc.ProgressMode, error) {
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
	if execUIOverrides.TTYOverride == nil {
		return tty
	}
	return *execUIOverrides.TTYOverride
}

func execProgressMode() catchrpc.ProgressMode {
	if execUIOverrides.Progress == "" {
		return catchrpc.ProgressAuto
	}
	return execUIOverrides.Progress
}
