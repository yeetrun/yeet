// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
)

var runHostSetFn = runHostSet

type hostStorageClient interface {
	HostStoragePlan(context.Context, catchrpc.HostStoragePlanRequest) (catchrpc.HostStoragePlan, error)
	HostStorageApply(context.Context, catchrpc.HostStorageApplyRequest) (catchrpc.HostStorageApplyResult, error)
	HostStorageFinalize(context.Context, catchrpc.HostStorageFinalizeRequest) (catchrpc.HostStorageFinalizeResult, error)
	HostStorageCleanup(context.Context, catchrpc.HostStorageCleanupRequest) (catchrpc.HostStorageCleanupResult, error)
}

type isoPoolClient interface {
	ISOPoolPlan(context.Context, catchrpc.ISOPoolPlanRequest) (catchrpc.ISOPoolPlan, error)
	ISOPoolApply(context.Context, catchrpc.ISOPoolApplyRequest) (catchrpc.ISOPoolApplyResult, error)
}

var (
	newHostStorageClientFn = func(host string) hostStorageClient {
		return newRPCClient(host)
	}
	newISOPoolClientFn = func(host string) isoPoolClient {
		return newRPCClient(host)
	}
	confirmHostSetFn                      = cmdutil.Confirm
	hostSetStdin                io.Reader = os.Stdin
	hostSetStdout               io.Writer = os.Stdout
	hostStorageReconnectNowFn             = time.Now
	hostStorageReconnectSleepFn           = func(ctx context.Context, delay time.Duration) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
)

const (
	hostStorageReconnectTimeout = 60 * time.Second
	hostStorageReconnectInitial = 100 * time.Millisecond
	hostStorageReconnectMaximum = 2 * time.Second
)

func HandleHostSet(ctx context.Context, args []string) error {
	args = trimHostSetSubcommand(args)
	flags, remaining, err := cli.ParseHostSet(args)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return fmt.Errorf("unexpected host set args: %s", strings.Join(remaining, " "))
	}
	return runHostSetFn(ctx, flags)
}

func trimHostSetSubcommand(args []string) []string {
	if len(args) > 0 && args[0] == "set" {
		return args[1:]
	}
	return args
}

func runHostSet(ctx context.Context, flags cli.HostSetFlags) error {
	if strings.TrimSpace(flags.ISOPool) != "" {
		return runHostSetISOPool(ctx, flags)
	}
	flags, err := completeHostSetFlags(flags)
	if err != nil {
		return err
	}
	req, err := hostStoragePlanRequest(flags)
	if err != nil {
		return err
	}
	host := Host()
	client := newHostStorageClientFn(host)
	plan, err := client.HostStoragePlan(ctx, req)
	if err != nil {
		return fmt.Errorf("plan host storage changes on %s: %w", host, err)
	}
	if err := renderHostStoragePlan(hostSetStdout, host, plan); err != nil {
		return err
	}
	if !hostStoragePlanHasChanges(plan) {
		return runHostSetNoChanges(host, plan, flags)
	}
	return runHostSetApply(ctx, client, host, plan, flags)
}

func runHostSetISOPool(ctx context.Context, flags cli.HostSetFlags) error {
	if hostSetHasStorageFlags(flags) {
		return fmt.Errorf("--iso-pool cannot be combined with host storage settings")
	}
	prefix, err := validateExplicitISOPool(flags.ISOPool)
	if err != nil {
		return err
	}
	host := Host()
	client := newISOPoolClientFn(host)
	plan, err := client.ISOPoolPlan(ctx, catchrpc.ISOPoolPlanRequest{Prefix: prefix.String()})
	if err != nil {
		return fmt.Errorf("plan ISO pool change on %s: %w", host, err)
	}
	if err := renderISOPoolPlan(hostSetStdout, host, plan); err != nil {
		return err
	}
	return applyISOPoolPlan(ctx, client, host, plan, flags)
}

func applyISOPoolPlan(ctx context.Context, client isoPoolClient, host string, plan catchrpc.ISOPoolPlan, flags cli.HostSetFlags) error {
	if err := blockedISOPoolPlanError(plan); err != nil {
		return err
	}
	if !plan.Changed {
		_, err := fmt.Fprintln(hostSetStdout, "No ISO pool changes to apply.")
		return err
	}
	if !flags.Yes {
		ok, confirmErr := confirmHostSetFn(hostSetStdin, hostSetStdout, "Apply ISO pool change now?")
		if confirmErr != nil {
			return confirmErr
		}
		if !ok {
			_, err := fmt.Fprintln(hostSetStdout, "Cancelled.")
			return err
		}
	}
	result, err := client.ISOPoolApply(ctx, catchrpc.ISOPoolApplyRequest{Plan: plan})
	if err != nil {
		return fmt.Errorf("apply ISO pool change on %s: %w", host, err)
	}
	_, err = fmt.Fprintf(hostSetStdout, "ISO pool set to %s (%s).\n", result.Prefix, result.Source)
	return err
}

func blockedISOPoolPlanError(plan catchrpc.ISOPoolPlan) error {
	var reasons []string
	if len(plan.Blockers) != 0 {
		reasons = append(reasons, "blockers: "+strings.Join(plan.Blockers, ", "))
	}
	if len(plan.Conflicts) != 0 {
		reasons = append(reasons, "conflicts: "+strings.Join(plan.Conflicts, ", "))
	}
	if len(reasons) == 0 {
		return nil
	}
	return fmt.Errorf("ISO pool change cannot be applied: %s", strings.Join(reasons, "; "))
}

func hostSetHasStorageFlags(flags cli.HostSetFlags) bool {
	return strings.TrimSpace(flags.DataDir) != "" ||
		strings.TrimSpace(flags.ServicesRoot) != "" ||
		flags.ZFS ||
		strings.TrimSpace(flags.MigrateServices) != "" ||
		strings.TrimSpace(flags.Config) != ""
}

func validateExplicitISOPool(raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 16 || prefix != prefix.Masked() {
		return netip.Prefix{}, fmt.Errorf("--iso-pool must be a canonical RFC1918 IPv4 /16")
	}
	private := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
	for _, allowed := range private {
		if allowed.Contains(prefix.Addr()) {
			return prefix, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("--iso-pool must be contained by RFC1918 space")
}

func renderISOPoolPlan(w io.Writer, host string, plan catchrpc.ISOPoolPlan) error {
	if _, err := fmt.Fprintf(w, "ISO pool plan for %s\n", host); err != nil {
		return err
	}
	for _, row := range []struct {
		label string
		value string
	}{
		{label: "Current", value: plan.CurrentPrefix},
		{label: "Desired", value: plan.DesiredPrefix},
		{label: "Source", value: plan.Source},
		{label: "Blockers", value: strings.Join(plan.Blockers, ", ")},
		{label: "Conflicts", value: strings.Join(plan.Conflicts, ", ")},
	} {
		if row.value == "" {
			continue
		}
		if _, err := fmt.Fprintf(w, "%s: %s\n", row.label, row.value); err != nil {
			return err
		}
	}
	return nil
}

func runHostSetNoChanges(host string, plan catchrpc.HostStoragePlan, flags cli.HostSetFlags) error {
	if _, err := fmt.Fprintln(hostSetStdout, "No host storage changes to apply."); err != nil {
		return err
	}
	return updateHostStorageConfig(hostSetStdout, flags.Config, host, plan, flags, catchrpc.HostStorageApplyResult{})
}

func runHostSetApply(ctx context.Context, client hostStorageClient, host string, plan catchrpc.HostStoragePlan, flags cli.HostSetFlags) error {
	apply, err := confirmHostStorageApply(flags, plan)
	if err != nil || !apply {
		return err
	}
	result, err := client.HostStorageApply(ctx, catchrpc.HostStorageApplyRequest{Plan: plan, Yes: true})
	if err != nil {
		return fmt.Errorf("apply host storage changes on %s: %w", host, err)
	}
	if err := renderHostStorageApplyResult(hostSetStdout, result); err != nil {
		return err
	}
	if result.TransactionID != "" {
		finalized, err := finalizeHostStorageAfterReconnect(ctx, client, host, result.TransactionID)
		if err != nil {
			return err
		}
		if err := renderHostStorageFinalizeResult(hostSetStdout, finalized); err != nil {
			return err
		}
	}
	return updateHostStorageConfig(hostSetStdout, flags.Config, host, plan, flags, result)
}

func finalizeHostStorageAfterReconnect(ctx context.Context, client hostStorageClient, host, transactionID string) (catchrpc.HostStorageFinalizeResult, error) {
	deadline := hostStorageReconnectNowFn().Add(hostStorageReconnectTimeout)
	retryCtx, cancel := context.WithTimeout(ctx, hostStorageReconnectTimeout)
	defer cancel()
	if _, err := fmt.Fprintf(hostSetStdout, "Host storage migration phase: reconnecting to Catch on %s.\n", host); err != nil {
		return catchrpc.HostStorageFinalizeResult{}, err
	}
	delay := hostStorageReconnectInitial
	var lastErr error
	for {
		if _, err := fmt.Fprintf(hostSetStdout, "Host storage migration phase: finalizing transaction %s.\n", transactionID); err != nil {
			return catchrpc.HostStorageFinalizeResult{}, err
		}
		result, err := client.HostStorageFinalize(retryCtx, catchrpc.HostStorageFinalizeRequest{TransactionID: transactionID})
		if err == nil {
			if result.TransactionID != transactionID {
				return catchrpc.HostStorageFinalizeResult{}, fmt.Errorf("finalize host storage on %s returned transaction %q, want %q", host, result.TransactionID, transactionID)
			}
			return result, nil
		}
		lastErr = err
		if !isHostStorageReconnectError(retryCtx, err) {
			return catchrpc.HostStorageFinalizeResult{}, fmt.Errorf("finalize host storage transaction %s on %s: %w", transactionID, host, err)
		}
		now := hostStorageReconnectNowFn()
		if !now.Before(deadline) {
			return catchrpc.HostStorageFinalizeResult{}, fmt.Errorf("catch on %s did not reconnect within 60s to finalize host storage transaction %s: %w", host, transactionID, lastErr)
		}
		remaining := deadline.Sub(now)
		wait := min(delay, remaining)
		if _, writeErr := fmt.Fprintf(hostSetStdout, "Catch is not ready; retrying in %s.\n", wait); writeErr != nil {
			return catchrpc.HostStorageFinalizeResult{}, writeErr
		}
		if err := hostStorageReconnectSleepFn(retryCtx, wait); err != nil {
			return catchrpc.HostStorageFinalizeResult{}, fmt.Errorf("wait for Catch on %s to reconnect: %w", host, errors.Join(lastErr, err))
		}
		delay = min(delay*2, hostStorageReconnectMaximum)
	}
}

func isHostStorageReconnectError(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return false
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE)
}

func renderHostStorageFinalizeResult(w io.Writer, result catchrpc.HostStorageFinalizeResult) error {
	if _, err := fmt.Fprintf(w, "Finalized host storage transaction %s.\n", result.TransactionID); err != nil {
		return err
	}
	if result.CleanupPending {
		_, err := fmt.Fprintln(w, "Cleanup pending for the inactive source host storage tree.")
		return err
	}
	return nil
}

func confirmHostStorageApply(flags cli.HostSetFlags, plan catchrpc.HostStoragePlan) (bool, error) {
	if !hostStoragePlanHasChanges(plan) {
		_, err := fmt.Fprintln(hostSetStdout, "No host storage changes to apply.")
		return false, err
	}
	if flags.Yes {
		return true, nil
	}
	ok, err := confirmHostSetFn(hostSetStdin, hostSetStdout, "Apply host storage changes now?")
	if err != nil {
		return false, err
	}
	if !ok {
		_, err := fmt.Fprintln(hostSetStdout, "Cancelled.")
		return false, err
	}
	return true, nil
}

func completeHostSetFlags(flags cli.HostSetFlags) (cli.HostSetFlags, error) {
	if strings.TrimSpace(flags.MigrateServices) != "" && strings.TrimSpace(flags.ServicesRoot) == "" {
		return flags, fmt.Errorf("--migrate-services requires --services-root")
	}
	if strings.TrimSpace(flags.ServicesRoot) == "" || strings.TrimSpace(flags.MigrateServices) != "" {
		return flags, nil
	}
	if flags.Yes {
		return flags, fmt.Errorf("--migrate-services=all|none is required with --yes when changing --services-root")
	}
	ok, err := confirmHostSetFn(hostSetStdin, hostSetStdout, "Migrate services currently under the existing services root?")
	if err != nil {
		return flags, err
	}
	if ok {
		flags.MigrateServices = string(catchrpc.HostStorageMigrateAll)
	} else {
		flags.MigrateServices = string(catchrpc.HostStorageMigrateNone)
	}
	return flags, nil
}

func hostStoragePlanRequest(flags cli.HostSetFlags) (catchrpc.HostStoragePlanRequest, error) {
	if strings.TrimSpace(flags.DataDir) == "" && strings.TrimSpace(flags.ServicesRoot) == "" {
		return catchrpc.HostStoragePlanRequest{}, fmt.Errorf("host set requires --data-dir or --services-root")
	}
	set := catchrpc.HostStorageSetRequest{
		MigrateServices: catchrpc.HostStorageMigrateServices(strings.TrimSpace(flags.MigrateServices)),
	}
	if dataDir := strings.TrimSpace(flags.DataDir); dataDir != "" {
		set.DataDir = &catchrpc.HostStorageTarget{Value: dataDir, ZFS: flags.ZFS}
	}
	if servicesRoot := strings.TrimSpace(flags.ServicesRoot); servicesRoot != "" {
		set.ServicesRoot = &catchrpc.HostStorageTarget{Value: servicesRoot, ZFS: flags.ZFS}
	}
	return catchrpc.HostStoragePlanRequest{Set: set}, nil
}

func hostStoragePlanHasChanges(plan catchrpc.HostStoragePlan) bool {
	return plan.DataDirAction.Move ||
		plan.CatchAction.Move ||
		hostStorageRepairActionHasChanges(plan.RepairAction) ||
		plan.RequiresRestart ||
		len(plan.ServicesAction.AffectedServices) != 0 ||
		len(plan.ZFSDatasetsToCreate) != 0 ||
		!hostSetPathsEqual(plan.Current.DataDir, plan.Desired.DataDir) ||
		!hostSetPathsEqual(plan.Current.ServicesRoot, plan.Desired.ServicesRoot)
}

func hostStorageRepairActionHasChanges(action catchrpc.HostStorageRepairAction) bool {
	return action.References != 0 ||
		action.DatabaseRefs != 0 ||
		action.SystemdRefs != 0 ||
		action.ArtifactRefs != 0 ||
		len(action.RegenerateUnits) != 0 ||
		len(action.RestartServices) != 0 ||
		len(action.ValidationRoots) != 0
}

func hostSetPathsEqual(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return a == b
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func renderHostStoragePlan(w io.Writer, host string, plan catchrpc.HostStoragePlan) error {
	if _, err := fmt.Fprintf(w, "Host storage plan for %s\n", host); err != nil {
		return err
	}
	return renderHostStoragePlanDetails(w, plan)
}

func renderHostStoragePlanDetails(w io.Writer, plan catchrpc.HostStoragePlan) error {
	if err := renderHostStoragePathChange(w, "Data dir", plan.Current.DataDir, plan.Desired.DataDir, plan.Desired.DataDirZFS); err != nil {
		return err
	}
	if err := renderHostStoragePathChange(w, "Services root", plan.Current.ServicesRoot, plan.Desired.ServicesRoot, plan.Desired.ServicesZFS); err != nil {
		return err
	}
	if len(plan.ZFSDatasetsToCreate) != 0 {
		if _, err := fmt.Fprintf(w, "Create ZFS datasets: %s\n", strings.Join(plan.ZFSDatasetsToCreate, ", ")); err != nil {
			return err
		}
	}
	if err := renderHostStorageServicesAction(w, plan.ServicesAction); err != nil {
		return err
	}
	if err := renderHostStorageCatchAction(w, plan.CatchAction); err != nil {
		return err
	}
	if err := renderHostStorageRepairAction(w, plan.RepairAction); err != nil {
		return err
	}
	return renderHostStoragePlanWarningsAndRestart(w, plan)
}

func renderHostStoragePlanWarningsAndRestart(w io.Writer, plan catchrpc.HostStoragePlan) error {
	for _, warning := range plan.Warnings {
		if _, err := fmt.Fprintf(w, "Warning: %s\n", warning); err != nil {
			return err
		}
	}
	if plan.RequiresRestart {
		_, err := fmt.Fprintln(w, "Catch restart required.")
		return err
	}
	return nil
}

func renderHostStoragePathChange(w io.Writer, label string, from string, to string, zfs bool) error {
	if hostSetPathsEqual(from, to) {
		return nil
	}
	suffix := ""
	if zfs {
		suffix = " (ZFS)"
	}
	_, err := fmt.Fprintf(w, "%s: %s -> %s%s\n", label, from, to, suffix)
	return err
}

func renderHostStorageCatchAction(w io.Writer, action catchrpc.HostStorageCatchAction) error {
	if !action.Move {
		return nil
	}
	_, err := fmt.Fprintf(w, "Catch service root: %s -> %s\n", action.From, action.To)
	return err
}

func renderHostStorageServicesAction(w io.Writer, action catchrpc.HostStorageServicesAction) error {
	if len(action.AffectedServices) == 0 {
		return nil
	}
	switch action.Mode {
	case catchrpc.HostStorageMigrateAll:
		if _, err := fmt.Fprintf(w, "Migrate services: %d\n", len(action.AffectedServices)); err != nil {
			return err
		}
	case catchrpc.HostStorageMigrateNone:
		if _, err := fmt.Fprintf(w, "Keep services at current roots: %d\n", len(action.AffectedServices)); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintf(w, "Service root updates: %d\n", len(action.AffectedServices)); err != nil {
			return err
		}
	}
	for _, move := range action.AffectedServices {
		if _, err := fmt.Fprintf(w, "  %s: %s -> %s\n", move.Name, move.From, move.To); err != nil {
			return err
		}
	}
	return nil
}

func renderHostStorageRepairAction(w io.Writer, action catchrpc.HostStorageRepairAction) error {
	if !hostStorageRepairActionHasChanges(action) {
		return nil
	}
	if err := renderHostStorageRepairReferenceCount(w, action); err != nil {
		return err
	}
	if err := renderHostStorageRepairRefCounts(w, action); err != nil {
		return err
	}
	if err := renderHostStorageRepairNames(w, "Regenerate systemd units", action.RegenerateUnits); err != nil {
		return err
	}
	if err := renderHostStorageRepairNames(w, "Restart services", action.RestartServices); err != nil {
		return err
	}
	return renderHostStorageRepairRoots(w, action.ValidationRoots)
}

func renderHostStorageRepairReferenceCount(w io.Writer, action catchrpc.HostStorageRepairAction) error {
	if action.References > 0 {
		_, err := fmt.Fprintf(w, "Repair host storage references: %d\n", action.References)
		return err
	}
	_, err := fmt.Fprintln(w, "Repair host storage references")
	return err
}

func renderHostStorageRepairRefCounts(w io.Writer, action catchrpc.HostStorageRepairAction) error {
	if action.DatabaseRefs > 0 {
		if _, err := fmt.Fprintf(w, "  Database refs: %d\n", action.DatabaseRefs); err != nil {
			return err
		}
	}
	if action.SystemdRefs > 0 {
		if _, err := fmt.Fprintf(w, "  Systemd refs: %d\n", action.SystemdRefs); err != nil {
			return err
		}
	}
	if action.ArtifactRefs > 0 {
		if _, err := fmt.Fprintf(w, "  Generated artifact refs: %d\n", action.ArtifactRefs); err != nil {
			return err
		}
	}
	return nil
}

func renderHostStorageRepairNames(w io.Writer, label string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	_, err := fmt.Fprintf(w, "  %s: %d (%s)\n", label, len(names), hostStorageNameSummary(names))
	return err
}

func renderHostStorageRepairRoots(w io.Writer, roots []string) error {
	if len(roots) == 0 {
		return nil
	}
	_, err := fmt.Fprintf(w, "  Validate old roots: %s\n", hostStorageNameSummary(roots))
	return err
}

func hostStorageNameSummary(names []string) string {
	const limit = 3
	if len(names) <= limit {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(names[:limit], ", "), len(names)-limit)
}

func renderHostStorageApplyResult(w io.Writer, result catchrpc.HostStorageApplyResult) error {
	for _, move := range result.MigratedServices {
		if _, err := fmt.Fprintf(w, "Updated service root for %s: %s -> %s\n", move.Name, move.From, move.To); err != nil {
			return err
		}
	}
	switch {
	case result.RestartScheduled:
		_, err := fmt.Fprintln(w, "Scheduled catch restart.")
		return err
	case result.Restarted:
		_, err := fmt.Fprintln(w, "Restarted catch.")
		return err
	default:
		return nil
	}
}

func updateHostStorageConfig(w io.Writer, configPath string, host string, plan catchrpc.HostStoragePlan, flags cli.HostSetFlags, result catchrpc.HostStorageApplyResult) error {
	canReconcileZFSRoot := hostStorageConfigCanReconcileZFSRoot(plan, flags)
	if !hostStorageConfigUpdateNeeded(result, canReconcileZFSRoot) {
		return nil
	}
	loc, err := hostSetConfigLocation(configPath)
	if err != nil {
		return err
	}
	if loc == nil || loc.Config == nil {
		_, err := fmt.Fprintf(w, "Skipped yeet.toml update: no %s found; pass --config to update service roots.\n", projectConfigName)
		return err
	}
	updated, skipped := applyHostStorageConfigUpdates(loc.Config, host, plan, flags, result, canReconcileZFSRoot)
	return saveAndReportHostStorageConfigUpdate(w, loc, updated, skipped)
}

func hostStorageConfigCanReconcileZFSRoot(plan catchrpc.HostStoragePlan, flags cli.HostSetFlags) bool {
	return plan.Desired.ServicesZFS && flags.ZFS && strings.TrimSpace(flags.ServicesRoot) != ""
}

func hostStorageConfigUpdateNeeded(result catchrpc.HostStorageApplyResult, canReconcileZFSRoot bool) bool {
	return len(result.MigratedServices) != 0 || canReconcileZFSRoot
}

func applyHostStorageConfigUpdates(cfg *ProjectConfig, host string, plan catchrpc.HostStoragePlan, flags cli.HostSetFlags, result catchrpc.HostStorageApplyResult, canReconcileZFSRoot bool) (int, int) {
	updated, skipped := applyHostStorageConfigMoves(cfg, host, plan.Desired.ServicesRoot, result.MigratedServices)
	if canReconcileZFSRoot {
		updated += applyHostStorageConfigZFSRootDefaults(cfg, host, plan.Desired.ServicesRoot, flags.ServicesRoot)
	}
	return updated, skipped
}

func saveAndReportHostStorageConfigUpdate(w io.Writer, loc *projectConfigLocation, updated, skipped int) error {
	if updated > 0 {
		if err := saveProjectConfig(loc); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "Updated %d service root%s in %s.\n", updated, pluralSuffix(updated), loc.Path); err != nil {
			return err
		}
	}
	if skipped > 0 {
		if _, err := fmt.Fprintf(w, "Skipped %d service root%s not present in %s.\n", skipped, pluralSuffix(skipped), loc.Path); err != nil {
			return err
		}
	}
	return nil
}

func hostSetConfigLocation(configPath string) (*projectConfigLocation, error) {
	if strings.TrimSpace(configPath) != "" {
		return loadProjectConfigFromFile(configPath)
	}
	return loadProjectConfigForCommandFromCwd()
}

func applyHostStorageConfigMoves(cfg *ProjectConfig, host string, desiredServicesRoot string, moves []catchrpc.HostStorageServiceMove) (int, int) {
	updated := 0
	skipped := 0
	for _, move := range moves {
		root, zfs := hostStorageConfigMoveRoot(desiredServicesRoot, move)
		if cfg.SetServiceRootForEntry(move.Name, host, root, zfs) {
			updated++
		} else {
			skipped++
		}
	}
	return updated, skipped
}

func hostStorageConfigMoveRoot(desiredServicesRoot string, move catchrpc.HostStorageServiceMove) (string, bool) {
	if strings.TrimSpace(move.ToZFS) != "" {
		return strings.TrimSpace(move.ToZFS), true
	}
	if strings.TrimSpace(desiredServicesRoot) != "" &&
		strings.TrimSpace(move.Name) != "" &&
		hostSetPathsEqual(move.To, filepath.Join(desiredServicesRoot, move.Name)) {
		return "", false
	}
	return move.To, false
}

func applyHostStorageConfigZFSRootDefaults(cfg *ProjectConfig, host string, desiredServicesRoot string, servicesRootDataset string) int {
	if cfg == nil || strings.TrimSpace(host) == "" || strings.TrimSpace(desiredServicesRoot) == "" || strings.TrimSpace(servicesRootDataset) == "" {
		return 0
	}
	updated := 0
	for i := range cfg.Services {
		if applyHostStorageConfigZFSRootDefault(&cfg.Services[i], host, desiredServicesRoot, servicesRootDataset) {
			updated++
		}
	}
	if updated > 0 {
		sortServiceEntries(cfg.Services)
	}
	return updated
}

func applyHostStorageConfigZFSRootDefault(entry *ServiceEntry, host string, desiredServicesRoot string, servicesRootDataset string) bool {
	if entry == nil || entry.Host != host || strings.TrimSpace(entry.Name) == "" {
		return false
	}
	wantDataset := path.Join(strings.TrimSpace(servicesRootDataset), entry.Name)
	if entry.ServiceRootZFS && strings.TrimSpace(entry.ServiceRoot) == wantDataset {
		return false
	}
	if !hostStorageConfigEntryMatchesServicesRoot(*entry, desiredServicesRoot, wantDataset) {
		return false
	}
	entry.ServiceRoot = wantDataset
	entry.ServiceRootZFS = true
	return true
}

func hostStorageConfigEntryMatchesServicesRoot(entry ServiceEntry, desiredServicesRoot string, datasetRoot string) bool {
	root := strings.TrimSpace(entry.ServiceRoot)
	if root == "" {
		return false
	}
	if root == datasetRoot {
		return true
	}
	return hostSetPathsEqual(root, filepath.Join(desiredServicesRoot, entry.Name))
}

func pluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
