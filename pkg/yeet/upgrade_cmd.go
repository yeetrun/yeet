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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/yeetrun/yeet/pkg/buildinfo"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
)

var buildUpgradeReportFn = buildUpgradeReport
var upgradeLocalBinaryFn = upgradeLocalBinary

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
	cfgLoc, _ := loadProjectConfigForCommandFromCwd()
	_, hasHostOverride := HardHostOverride()
	hosts := upgradeKnownHosts(cfgLoc, hasHostOverride)
	report := buildUpgradeReportFn(ctx, upgradeCheckRequest{
		Local:         local,
		Hosts:         hosts,
		Now:           time.Now(),
		Force:         flags.Force,
		Nightly:       flags.Nightly,
		TargetVersion: flags.Version,
	})
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
	versionHeader := "LATEST"
	if upgradeReportUsesTarget(report) {
		versionHeader = "TARGET"
	}
	if _, err := fmt.Fprintf(tw, "COMPONENT\tCURRENT\t%s\tSTATUS\n", versionHeader); err != nil {
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

func upgradeReportUsesTarget(report upgradeReport) bool {
	return report.Force || upgradeReportTargetsNightly(report) || strings.TrimSpace(report.TargetVersion) != ""
}

func upgradeReportTargetsNightly(report upgradeReport) bool {
	return report.Nightly || report.Latest.Nightly
}

func upgradeReportReleaseVersion(report upgradeReport) string {
	if upgradeReportTargetsNightly(report) {
		return ""
	}
	return report.Latest.Tag
}

func renderUpgradeRow(w io.Writer, row upgradeComponent) error {
	component := row.Name
	if row.Host != "" {
		component += "@" + row.Host
	}
	current := row.Current
	if current == "" {
		current = "-"
	}
	latest := row.Latest
	if latest == "" {
		latest = "-"
	}
	status := string(row.Status)
	if row.Reason != "" && row.Status != upgradeStatusDev {
		status += ": " + row.Reason
	}
	_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", component, current, latest, status)
	return err
}

func runUpgrade(ctx context.Context, stdout io.Writer, stderr io.Writer, flags cli.UpgradeFlags, report upgradeReport) error {
	proceed, err := confirmUpgradeIfNeeded(os.Stdin, stdout, stderr, flags, report)
	if err != nil || !proceed {
		return err
	}
	if err := upgradeLocalFromReport(flags, report); err != nil {
		return err
	}
	return upgradeCatchFromReport(ctx, report)
}

func confirmUpgradeIfNeeded(stdin io.Reader, stdout io.Writer, stderr io.Writer, flags cli.UpgradeFlags, report upgradeReport) (bool, error) {
	if !upgradeReportHasUpdates(report) {
		if _, err := fmt.Fprintln(stdout, "No upgrades available."); err != nil {
			return false, err
		}
		return false, nil
	}
	if flags.Yes {
		return true, nil
	}
	ok, err := confirmUpgradePlan(stdin, stdout, report)
	if err != nil || ok {
		return ok, err
	}
	if _, err := fmt.Fprintln(stderr, "Upgrade cancelled"); err != nil {
		return false, err
	}
	return false, nil
}

func upgradeReportHasUpdates(report upgradeReport) bool {
	if upgradeRowActionable(report.Local) {
		return true
	}
	for _, row := range report.Catch {
		if upgradeRowActionable(row) {
			return true
		}
	}
	return false
}

func upgradeRowActionable(row upgradeComponent) bool {
	return row.Status == upgradeStatusUpdateAvailable || row.Status == upgradeStatusReinstall
}

func upgradeLocalFromReport(flags cli.UpgradeFlags, report upgradeReport) error {
	if upgradeRowActionable(report.Local) {
		latest := report.Latest
		if flags.Nightly || upgradeReportTargetsNightly(report) {
			latest.Nightly = true
		}
		if err := upgradeLocalBinaryFn(buildinfo.Current(), latest, flags.Force); err != nil {
			return err
		}
	}
	return nil
}

func upgradeCatchFromReport(ctx context.Context, report upgradeReport) error {
	for _, row := range report.Catch {
		if !upgradeRowActionable(row) {
			continue
		}
		target, err := catchInstallTarget(row)
		if err != nil {
			return err
		}
		if err := withTemporaryHost(row.Host, func() error {
			return initCatchFn(target, initOptions{fromGithub: true, nightly: upgradeReportTargetsNightly(report), noWorkspace: true, suppressNextSteps: true, releaseVersion: upgradeReportReleaseVersion(report)})
		}); err != nil {
			return fmt.Errorf("upgrade catch@%s: %w", row.Host, err)
		}
	}
	return nil
}

func confirmUpgradePlan(stdin io.Reader, stdout io.Writer, report upgradeReport) (bool, error) {
	if _, err := fmt.Fprintln(stdout, "Upgrade plan:"); err != nil {
		return false, err
	}
	if upgradeRowActionable(report.Local) {
		if _, err := fmt.Fprintf(stdout, "  yeet: %s -> %s%s\n", report.Local.Current, report.Local.Latest, upgradePlanStatusSuffix(report.Local)); err != nil {
			return false, err
		}
	}
	for _, row := range report.Catch {
		if upgradeRowActionable(row) {
			if _, err := fmt.Fprintf(stdout, "  catch@%s: %s -> %s%s\n", row.Host, row.Current, row.Latest, upgradePlanStatusSuffix(row)); err != nil {
				return false, err
			}
		}
	}
	return cmdutil.Confirm(stdin, stdout, "Proceed?")
}

func upgradePlanStatusSuffix(row upgradeComponent) string {
	if row.Status == upgradeStatusReinstall {
		return " (reinstall release)"
	}
	return ""
}

func catchInstallTarget(row upgradeComponent) (string, error) {
	host := strings.TrimSpace(row.InstallHost)
	user := strings.TrimSpace(row.InstallUser)
	if host == "" {
		return "", fmt.Errorf("catch@%s missing install host metadata; run yeet init root@the-ssh-machine-host --from-github", row.Host)
	}
	if strings.Contains(host, "@") {
		return host, nil
	}
	if user == "" {
		return "", fmt.Errorf("catch@%s missing install user metadata; run yeet init root@%s --from-github", row.Host, host)
	}
	return user + "@" + host, nil
}
