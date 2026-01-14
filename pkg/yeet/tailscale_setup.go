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
	"github.com/shayne/yeet/pkg/catchrpc"
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

	secret := strings.TrimSpace(flags.ClientSecret)
	if secret == "" {
		if !isTerminalFn(int(os.Stdin.Fd())) {
			return fmt.Errorf("client secret is required (use --client-secret or run in a TTY)")
		}
		secret, err = promptTailscaleClientSecret(os.Stdout, os.Stdin)
		if err != nil {
			return err
		}
		secret = strings.TrimSpace(secret)
	}
	if secret == "" {
		return fmt.Errorf("client secret is required")
	}
	if !strings.HasPrefix(secret, "tskey-client-") {
		return fmt.Errorf("invalid client secret (expected tskey-client-...)")
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
	fmt.Fprintf(os.Stdout, "Tailscale client secret stored on %s (%s).\n", Host(), resp.Path)
	return nil
}

func promptTailscaleClientSecret(out io.Writer, in io.Reader) (string, error) {
	fmt.Fprintln(out, "Tailscale OAuth setup")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "1) Create a tag for yeet services:")
	fmt.Fprintln(out, "   https://login.tailscale.com/admin/acls/visual/tags")
	fmt.Fprintln(out, "   Example: tag:app")
	fmt.Fprintln(out, "2) Create a trust credential (OAuth client):")
	fmt.Fprintln(out, "   https://login.tailscale.com/admin/settings/trust-credentials")
	fmt.Fprintln(out, "   - Scope: Devices -> Core (write)")
	fmt.Fprintln(out, "   - Tags: select the tag you created (e.g. tag:app)")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Client secret:")
	fmt.Fprintln(out, "Paste the client secret and press Enter when done.")
	fmt.Fprint(out, "> ")

	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
