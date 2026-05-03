// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"os"
	"testing"

	"github.com/fatih/color"
)

func TestSkirtStopsWhenContextCancelled(t *testing.T) {
	oldStdout := os.Stdout
	oldColorOutput := color.Output
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile devnull error: %v", err)
	}
	os.Stdout = devNull
	color.Output = devNull
	t.Cleanup(func() {
		os.Stdout = oldStdout
		color.Output = oldColorOutput
		_ = devNull.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := HandleSkirt(ctx, nil); err != nil {
		t.Fatalf("HandleSkirt error: %v", err)
	}
}
