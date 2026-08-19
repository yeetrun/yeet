// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/hugomd/ascii-live/frames"
	"github.com/yeetrun/yeet/pkg/tui"
)

func HandleSkirt(ctx context.Context, _ []string) error {
	styles := []lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Red),
		lipgloss.NewStyle().Foreground(lipgloss.Green),
		lipgloss.NewStyle().Foreground(lipgloss.Yellow),
		lipgloss.NewStyle().Foreground(lipgloss.Blue),
		lipgloss.NewStyle().Foreground(lipgloss.Magenta),
		lipgloss.NewStyle().Foreground(lipgloss.Cyan),
		lipgloss.NewStyle().Foreground(lipgloss.White),
	}
	colorPolicy := tui.NewStyles(true)
	p := frames.Parrot
	x := 0
	for {
		fmt.Print("\033[H\033[2J")
		x++
		i := x % p.GetLength()
		style := styles[x%len(styles)]
		fmt.Println(renderSkirtFrame(colorPolicy, style, p.GetFrame(i)))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(p.GetSleep()):
			continue
		}
	}
}

func renderSkirtFrame(colorPolicy tui.Styles, style lipgloss.Style, frame string) string {
	if !colorPolicy.Enabled() {
		return frame
	}
	return style.Render(frame)
}
