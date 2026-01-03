// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"time"

	"github.com/fatih/color"
	"github.com/hugomd/ascii-live/frames"
)

func HandleSkirt(ctx context.Context, _ []string) error {
	colors := []*color.Color{
		color.New(color.FgRed),
		color.New(color.FgGreen),
		color.New(color.FgYellow),
		color.New(color.FgBlue),
		color.New(color.FgMagenta),
		color.New(color.FgCyan),
		color.New(color.FgWhite),
	}
	p := frames.Parrot
	x := 0
	for {
		fmt.Print("\033[H\033[2J")
		x++
		i := x % p.GetLength()
		c := colors[x%len(colors)]
		c.Println(p.GetFrame(i))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(p.GetSleep()):
			continue
		}
	}
}
