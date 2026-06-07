// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/yeetrun/yeet/pkg/buildinfo"
	"github.com/yeetrun/yeet/pkg/cli"
)

var buildUpgradeReportFn = buildUpgradeReport

func HandleUpgrade(ctx context.Context, args []string) error {
	return handleUpgrade(ctx, args, os.Stdout, os.Stderr, buildinfo.Current())
}

func handleUpgrade(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, local buildinfo.Info) error {
	if len(args) > 0 && args[0] == "upgrade" {
		args = args[1:]
	}
	flags, pos, err := cli.ParseUpgrade(args)
	if err != nil {
		return err
	}
	if flags.Host != "" {
		SetHostOverride(flags.Host)
	}
	checkOnly := flags.Check || len(pos) > 0 && pos[0] == "check"
	cfgLoc, _ := loadProjectConfigFromCwd()
	_, hasHostOverride := HostOverride()
	hosts := upgradeKnownHosts(cfgLoc, flags.All, hasHostOverride)
	report := buildUpgradeReportFn(ctx, upgradeCheckRequest{Local: local, Hosts: hosts, Now: time.Now()})
	if checkOnly {
		if flags.JSON {
			return json.NewEncoder(stdout).Encode(report)
		}
		return renderUpgradeReport(stdout, report)
	}
	return runUpgrade(ctx, stdout, stderr, flags, report)
}

func renderUpgradeReport(w io.Writer, report upgradeReport) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "COMPONENT\tCURRENT\tLATEST\tSTATUS"); err != nil {
		return err
	}
	if err := renderUpgradeRow(tw, report.Local); err != nil {
		return err
	}
	for _, row := range report.Catch {
		if err := renderUpgradeRow(tw, row); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func renderUpgradeRow(w io.Writer, row upgradeComponent) error {
	component := row.Name
	if row.Host != "" {
		component += "@" + row.Host
	}
	status := string(row.Status)
	if row.Reason != "" {
		status += ": " + row.Reason
	}
	_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", component, row.Current, row.Latest, status)
	return err
}

func runUpgrade(context.Context, io.Writer, io.Writer, cli.UpgradeFlags, upgradeReport) error {
	return fmt.Errorf("upgrade apply path is unavailable in this build")
}
