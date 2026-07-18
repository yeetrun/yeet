// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"golang.org/x/term"
)

var (
	confirmHostCleanupFn               = cmdutil.Confirm
	hostCleanupStdin         io.Reader = os.Stdin
	hostCleanupStdout        io.Writer = os.Stdout
	hostCleanupInteractiveFn           = func() bool {
		file, ok := hostCleanupStdin.(*os.File)
		return ok && term.IsTerminal(int(file.Fd()))
	}
)

func HandleHostCleanup(ctx context.Context, args []string) error {
	args = trimHostCleanupSubcommand(args)
	flags, remaining, err := cli.ParseHostCleanup(args)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return fmt.Errorf("unexpected host cleanup args: %s", strings.Join(remaining, " "))
	}
	return runHostCleanup(ctx, flags)
}

func trimHostCleanupSubcommand(args []string) []string {
	if len(args) > 0 && args[0] == "cleanup" {
		return args[1:]
	}
	return args
}

func runHostCleanup(ctx context.Context, flags cli.HostCleanupFlags) error {
	source := strings.TrimSpace(flags.From)
	if source == "" {
		return fmt.Errorf("host cleanup requires --from=PATH")
	}
	if !filepath.IsAbs(source) {
		return fmt.Errorf("host cleanup --from must be an absolute path, got %q", source)
	}
	source = filepath.Clean(source)
	if !flags.Yes {
		if !hostCleanupInteractiveFn() {
			return fmt.Errorf("host cleanup of %q requires --yes in non-interactive use", source)
		}
		if _, err := fmt.Fprintf(hostCleanupStdout, "Host storage cleanup will permanently remove: %s\n", source); err != nil {
			return err
		}
		confirmed, err := confirmHostCleanupFn(hostCleanupStdin, hostCleanupStdout, fmt.Sprintf("Permanently remove host storage at %s?", source))
		if err != nil {
			return err
		}
		if !confirmed {
			_, err := fmt.Fprintln(hostCleanupStdout, "Cancelled.")
			return err
		}
	}
	host := Host()
	result, err := newHostStorageClientFn(host).HostStorageCleanup(ctx, catchrpc.HostStorageCleanupRequest{From: source, Yes: true})
	if err != nil {
		return fmt.Errorf("clean host storage on %s: %w", host, err)
	}
	_, err = fmt.Fprintf(hostCleanupStdout, "Removed host storage %s (transaction %s).\n", result.Removed, result.TransactionID)
	return err
}
