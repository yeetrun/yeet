// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shayne/yargs"
	"github.com/yeetrun/yeet/pkg/catchrpc"
)

type tailscaleSetupFlagsParsed struct {
	Setup        bool   `flag:"setup" help:"Interactive tailscale OAuth setup for the catch host"`
	ClientSecret string `flag:"client-secret" help:"Tailscale OAuth client secret (tskey-client-...)"`
}

func parseTailscaleSetupFlags(args []string) (tailscaleSetupFlagsParsed, []string, error) {
	if len(args) > 0 && args[0] == "tailscale" {
		args = args[1:]
	}
	result, err := yargs.ParseKnownFlags[tailscaleSetupFlagsParsed](args, yargs.KnownFlagsOptions{})
	if err != nil {
		return tailscaleSetupFlagsParsed{}, nil, err
	}
	return result.Flags, result.RemainingArgs, nil
}

func HandleTailscale(ctx context.Context, args []string) error {
	flags, remaining, err := parseTailscaleSetupFlags(args)
	if err != nil {
		return err
	}
	if !flags.Setup {
		if flags.ClientSecret != "" {
			return fmt.Errorf("--client-secret requires --setup")
		}
		return HandleSvcCmd(args)
	}
	if len(remaining) > 0 {
		return fmt.Errorf("tailscale --setup does not accept additional arguments")
	}

	secret, err := resolveTailscaleClientSecret(
		flags.ClientSecret,
		isTerminalFn(int(os.Stdin.Fd())),
		os.Stdout,
		os.Stdin,
	)
	if err != nil {
		return err
	}

	var resp catchrpc.TailscaleSetupResponse
	if err := newRPCClient(Host()).Call(ctx, "catch.TailscaleSetup", catchrpc.TailscaleSetupRequest{
		ClientSecret: secret,
	}, &resp); err != nil {
		return err
	}
	if !resp.Verified {
		return fmt.Errorf("tailscale secret written but verification failed")
	}
	_, err = fmt.Fprintf(os.Stdout, "Tailscale client secret stored on %s (%s).\n", Host(), resp.Path)
	return err
}

func resolveTailscaleClientSecret(secret string, interactive bool, out io.Writer, in io.Reader) (string, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		if !interactive {
			return "", fmt.Errorf("client secret is required (use --client-secret or run in a TTY)")
		}
		prompted, err := promptTailscaleClientSecret(out, in)
		if err != nil {
			return "", err
		}
		secret = strings.TrimSpace(prompted)
	}
	if secret == "" {
		return "", fmt.Errorf("client secret is required")
	}
	if !strings.HasPrefix(secret, "tskey-client-") {
		return "", fmt.Errorf("invalid client secret (expected tskey-client-...)")
	}
	return secret, nil
}

func promptTailscaleClientSecret(out io.Writer, in io.Reader) (string, error) {
	for _, line := range []string{
		"Tailscale OAuth setup",
		"",
		"1) Create a tag for yeet services:",
		"   https://login.tailscale.com/admin/acls/visual/tags",
		"   Example: tag:app",
		"2) Create a trust credential (OAuth client):",
		"   https://login.tailscale.com/admin/settings/trust-credentials",
		"   - Scope: Devices -> Core (write)",
		"   - Tags: select the tag you created (e.g. tag:app)",
		"",
		"Client secret:",
		"Paste the client secret and press Enter when done.",
	} {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return "", err
		}
	}
	if _, err := fmt.Fprint(out, "> "); err != nil {
		return "", err
	}

	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
