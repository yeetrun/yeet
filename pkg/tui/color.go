// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tui

import "os"

const (
	ColorReset  = "\x1b[0m"
	ColorRed    = "\x1b[31m"
	ColorGreen  = "\x1b[32m"
	ColorYellow = "\x1b[33m"
	ColorDim    = "\x1b[90m"
)

type Colorizer struct {
	Enabled bool
}

func NewColorizer(enabled bool) Colorizer {
	if !enabled {
		return Colorizer{}
	}
	if os.Getenv("NO_COLOR") != "" {
		return Colorizer{}
	}
	term := os.Getenv("TERM")
	if term == "" || term == "dumb" {
		return Colorizer{}
	}
	return Colorizer{Enabled: true}
}

func (c Colorizer) Wrap(code, text string) string {
	if !c.Enabled || code == "" {
		return text
	}
	return code + text + ColorReset
}
