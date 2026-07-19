// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

const (
	defaultInitDataDir       = "/var/lib/yeet"
	defaultCustomInitDataDir = "/srv/yeet"
)

type initStorageOptions struct {
	DataDir             string
	DataDirZFS          bool
	ServicesRoot        string
	ServicesRootZFS     bool
	remoteCatchBinary   string
	existingCatch       bool
	legacyCleanupSource string
}

type initLegacyStorageCandidate struct {
	Eligible   bool
	SourceRoot string
	TargetRoot string
	Plan       catchrpc.HostStoragePlan
}

type initStorageProbe struct {
	Home               string
	ZFSAvailable       bool
	SuggestedZFSPrefix string
}

type initStorageWizardFunc func(io.Reader, io.Writer, initStorageProbe) (initStorageOptions, error)

var (
	prepareInitStorageOptionsFn                            = prepareInitStorageOptions
	runInitStorageWizardFn           initStorageWizardFunc = runInitStorageWizard
	remoteInitExistingCatchStorageFn                       = remoteInitExistingCatchStorage
	remoteInitStorageProbeFn                               = remoteInitStorageProbe
	remoteInitStorageCommandOKFn                           = remoteInitStorageCommandOK
	remoteInitStorageOutputFn                              = remoteInitStorageOutput
	initCatchRemoteBinaryCounter     atomic.Uint64
)

func initStorageOptionsFromFlags(flags initFlagsParsed) (initStorageOptions, error) {
	storage := initStorageOptions{
		DataDir:      strings.TrimSpace(flags.DataDir),
		ServicesRoot: strings.TrimSpace(flags.ServicesRoot),
	}
	if !flags.ZFS {
		return storage, nil
	}
	if storage.DataDir == "" && storage.ServicesRoot == "" {
		return initStorageOptions{}, fmt.Errorf("--zfs requires --data-dir or --services-root")
	}
	if storage.DataDir != "" {
		storage.DataDirZFS = true
	}
	if storage.ServicesRoot != "" {
		storage.ServicesRootZFS = true
	}
	return storage, nil
}

func (o initStorageOptions) explicit() bool {
	return strings.TrimSpace(o.DataDir) != "" ||
		strings.TrimSpace(o.ServicesRoot) != "" ||
		o.DataDirZFS ||
		o.ServicesRootZFS
}

func (o initStorageOptions) summary() string {
	if !o.explicit() {
		return "defaults"
	}
	parts := make([]string, 0, 2)
	if strings.TrimSpace(o.DataDir) != "" {
		label := "data dir " + o.DataDir
		if o.DataDirZFS {
			label = "data dataset " + o.DataDir
		}
		parts = append(parts, label)
	}
	if strings.TrimSpace(o.ServicesRoot) != "" {
		label := "services root " + o.ServicesRoot
		if o.ServicesRootZFS {
			label = "services dataset " + o.ServicesRoot
		}
		parts = append(parts, label)
	} else {
		parts = append(parts, "services under data dir")
	}
	return strings.Join(parts, "; ")
}

func withInitCatchRemoteBinary(storage initStorageOptions, useSudo bool) initStorageOptions {
	storage.remoteCatchBinary = initCatchRemoteBinaryPath(storage, useSudo)
	return storage
}

func (o initStorageOptions) catchRemoteBinary() string {
	if binary := strings.TrimSpace(o.remoteCatchBinary); binary != "" {
		return binary
	}
	return "./catch"
}

func initCatchRemoteBinaryPath(storage initStorageOptions, useSudo bool) string {
	if useSudo {
		return ""
	}
	servicesRoot := initCatchRemoteServicesRoot(storage)
	if servicesRoot == "" {
		return ""
	}
	return path.Join(servicesRoot, catchServiceName, "run", uniqueInitCatchInstallerName())
}

func uniqueInitCatchInstallerName() string {
	return fmt.Sprintf("catch.install.%d.%d.%d", os.Getpid(), time.Now().UnixNano(), initCatchRemoteBinaryCounter.Add(1))
}

func initCatchRemoteServicesRoot(storage initStorageOptions) string {
	if servicesRoot := strings.TrimSpace(storage.ServicesRoot); initRemoteAbsolutePath(servicesRoot) {
		return path.Clean(servicesRoot)
	}
	dataDir := strings.TrimSpace(storage.DataDir)
	if !initRemoteAbsolutePath(dataDir) {
		return ""
	}
	return path.Join(path.Clean(dataDir), "services")
}

func initRemoteAbsolutePath(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), "/")
}

func prepareInitStorageOptions(ui *initUI, userAtRemote string, useSudo bool, opts initOptions) (initStorageOptions, error) {
	if opts.storage.explicit() {
		return opts.storage, nil
	}
	ui.StartStep("Plan storage")
	existing, installed, err := remoteInitExistingCatchStorageFn(userAtRemote)
	if err != nil {
		ui.Warn(fmt.Sprintf("Warning: could not check existing catch install: %v", err))
	} else if installed {
		existing.existingCatch = true
		ui.DoneStep("existing catch")
		return existing, nil
	}
	if !canPromptInitStorage() {
		ui.DoneStep("defaults")
		return initStorageOptions{}, nil
	}
	probe, err := remoteInitStorageProbeFn(userAtRemote, useSudo)
	if err != nil {
		ui.Warn(fmt.Sprintf("Warning: could not inspect remote storage: %v", err))
		probe = initStorageProbe{Home: defaultInitStorageHome(useSudo)}
	}
	ui.Suspend()
	storage, err := runInitStorageWizardFn(os.Stdin, os.Stdout, probe)
	ui.Resume()
	if err != nil {
		ui.FailStep(err.Error())
		return initStorageOptions{}, err
	}
	ui.DoneStep(storage.summary())
	return storage, nil
}

func runInitLegacyStorageMigration(ui *initUI, userAtRemote string, storage initStorageOptions) error {
	if !storage.existingCatch || storage.DataDirZFS || storage.ServicesRootZFS {
		return nil
	}
	host := initLegacyStorageRPCHost(ui, userAtRemote)
	client := newHostStorageClientFn(host)
	if hostSetPathsEqual(storage.DataDir, defaultInitDataDir) {
		return resumeInitLegacyStorageCleanup(context.Background(), ui, client, storage)
	}
	candidate, err := planInitLegacyStorageMigration(context.Background(), client, userAtRemote, storage)
	if err != nil {
		return err
	}
	if !candidate.Eligible {
		return nil
	}
	return runInitLegacyStorageCandidate(ui, client, host, candidate)
}

func planInitLegacyStorageMigration(ctx context.Context, client hostStorageClient, userAtRemote string, storage initStorageOptions) (initLegacyStorageCandidate, error) {
	if !shouldPlanInitLegacyStorageMigration(storage) {
		return initLegacyStorageCandidate{}, nil
	}
	plan, err := client.HostStoragePlan(ctx, initLegacyStoragePlanRequest())
	if err != nil {
		return initLegacyStorageCandidate{}, fmt.Errorf("plan legacy host storage migration on %s: %w", userAtRemote, err)
	}
	return initLegacyStorageCandidateFromPlan(plan), nil
}

func runInitLegacyStorageCandidate(ui *initUI, client hostStorageClient, host string, candidate initLegacyStorageCandidate) error {
	if !canPromptInitStorage() {
		return writeInitLegacyStorageCommands(ui.out, candidate.SourceRoot)
	}
	if err := renderInitLegacyStoragePlan(ui.out, candidate); err != nil {
		return err
	}
	ui.Suspend()
	confirmed, err := activePrompter.Confirm("Move Yeet's legacy state to /var/lib/yeet and remove the old tree after verification? [Y/n]", true)
	ui.Resume()
	if err != nil {
		return fmt.Errorf("confirm legacy host storage migration: %w", err)
	}
	if !confirmed {
		return writeInitLegacyStorageCommands(ui.out, candidate.SourceRoot)
	}
	return applyInitLegacyStorageMigration(context.Background(), ui.out, client, host, candidate)
}

func initLegacyStorageRPCHost(ui *initUI, userAtRemote string) string {
	// SSH installs through the machine address, but Catch RPCs use the
	// configured Catch identity and may not be reachable on the machine LAN.
	if ui != nil {
		if host := strings.TrimSpace(ui.host); host != "" {
			return host
		}
	}
	_, host, ok := strings.Cut(strings.TrimSpace(userAtRemote), "@")
	if ok {
		return host
	}
	return strings.TrimSpace(userAtRemote)
}

func shouldPlanInitLegacyStorageMigration(storage initStorageOptions) bool {
	dataDir := strings.TrimSpace(storage.DataDir)
	return dataDir != "" && !hostSetPathsEqual(dataDir, defaultInitDataDir)
}

func resumeInitLegacyStorageCleanup(ctx context.Context, ui *initUI, client hostStorageClient, storage initStorageOptions) error {
	source, ok, err := normalizeInitLegacyStorageCleanupSource(storage.legacyCleanupSource)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if !canPromptInitStorage() {
		return writeInitLegacyStorageCleanupCommand(ui.out, source)
	}
	ui.Suspend()
	confirmed, err := activePrompter.Confirm(fmt.Sprintf("Remove inactive legacy Yeet state at %s? [Y/n]", source), true)
	ui.Resume()
	if err != nil {
		return fmt.Errorf("confirm inactive legacy host storage cleanup: %w", err)
	}
	if !confirmed {
		return writeInitLegacyStorageCleanupCommand(ui.out, source)
	}
	result, err := client.HostStorageCleanup(ctx, catchrpc.HostStorageCleanupRequest{From: source, Yes: true})
	if err != nil {
		return initLegacyStorageCleanupError(defaultInitDataDir, source, err)
	}
	if err := validateInitLegacyStorageCleanupResult(result, "", source); err != nil {
		return initLegacyStorageCleanupError(defaultInitDataDir, source, err)
	}
	_, err = fmt.Fprintf(ui.out, "Removed inactive legacy host storage %s (transaction %s).\n", result.Removed, result.TransactionID)
	return err
}

func normalizeInitLegacyStorageCleanupSource(source string) (string, bool, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", false, nil
	}
	source = filepath.Clean(source)
	if !filepath.IsAbs(source) || hostSetPathsEqual(source, defaultInitDataDir) {
		return "", false, fmt.Errorf("unsafe recorded legacy cleanup source %q", source)
	}
	return source, true, nil
}

func initLegacyStorageSourceFromRecordedHome(installHome string) (string, bool) {
	installHome = filepath.Clean(strings.TrimSpace(installHome))
	if installHome == "." || !filepath.IsAbs(installHome) {
		return "", false
	}
	source := filepath.Join(installHome, "yeet-data")
	return source, !hostSetPathsEqual(source, defaultInitDataDir)
}

func initLegacyStoragePlanRequest() catchrpc.HostStoragePlanRequest {
	return catchrpc.HostStoragePlanRequest{Set: catchrpc.HostStorageSetRequest{
		DataDir:         &catchrpc.HostStorageTarget{Value: defaultInitDataDir},
		ServicesRoot:    &catchrpc.HostStorageTarget{Value: path.Join(defaultInitDataDir, "services")},
		MigrateServices: catchrpc.HostStorageMigrateAll,
	}}
}

func initLegacyStorageCandidateFromPlan(plan catchrpc.HostStoragePlan) initLegacyStorageCandidate {
	return initLegacyStorageCandidate{
		Eligible:   plan.Legacy.Eligible && plan.Legacy.CleanupAllowed,
		SourceRoot: filepath.Clean(plan.Legacy.SourceRoot),
		TargetRoot: filepath.Clean(plan.Legacy.TargetRoot),
		Plan:       plan,
	}
}

func renderInitLegacyStoragePlan(w io.Writer, candidate initLegacyStorageCandidate) error {
	if _, err := fmt.Fprintln(w, "Legacy host storage migration plan"); err != nil {
		return err
	}
	if err := renderHostStoragePlanDetails(w, candidate.Plan); err != nil {
		return err
	}
	preserved := append([]string(nil), candidate.Plan.Legacy.PreservedRoots...)
	sort.Strings(preserved)
	if len(preserved) == 0 {
		preserved = []string{"none"}
	}
	if _, err := fmt.Fprintf(w, "Preserved service roots: %s\n", strings.Join(preserved, ", ")); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Estimated copy: %d bytes; target free space: %d bytes\n", candidate.Plan.Estimate.BytesToCopy, candidate.Plan.Estimate.BytesFree); err != nil {
		return err
	}
	running := initLegacyStorageRunningServices(candidate.Plan)
	if len(running) == 0 {
		running = []string{"none"}
	}
	if _, err := fmt.Fprintf(w, "Running services that will stop and restart: %s\n", strings.Join(running, ", ")); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w,
		"Rollback boundary: %s remains authoritative until Catch reconnects and validates %s; only then may the inactive legacy tree be removed.\n",
		candidate.SourceRoot,
		candidate.TargetRoot,
	)
	return err
}

func initLegacyStorageRunningServices(plan catchrpc.HostStoragePlan) []string {
	running := make([]string, 0, len(plan.ServicesAction.AffectedServices))
	for _, service := range plan.ServicesAction.AffectedServices {
		if service.WasRunning {
			running = append(running, service.Name)
		}
	}
	sort.Strings(running)
	return running
}

func writeInitLegacyStorageCommands(w io.Writer, source string) error {
	_, err := fmt.Fprintf(w,
		"Legacy Yeet state remains at %s.\n"+
			"Migrate it explicitly:\n"+
			"  yeet host set --data-dir=/var/lib/yeet --services-root=/var/lib/yeet/services --migrate-services=all --yes\n"+
			"After Catch reconnects and validates the new state, remove the inactive legacy tree:\n"+
			"  yeet host cleanup --from=%s --yes\n",
		source,
		source,
	)
	return err
}

func writeInitLegacyStorageCleanupCommand(w io.Writer, source string) error {
	_, err := fmt.Fprintf(w,
		"Inactive legacy Yeet state remains at %s.\n"+
			"Remove it explicitly:\n"+
			"  yeet host cleanup --from=%s --yes\n",
		source,
		source,
	)
	return err
}

func applyInitLegacyStorageMigration(ctx context.Context, w io.Writer, client hostStorageClient, host string, candidate initLegacyStorageCandidate) error {
	result, err := client.HostStorageApply(ctx, catchrpc.HostStorageApplyRequest{Plan: candidate.Plan, Yes: true})
	if err != nil {
		return initLegacyStorageApplyError(candidate.SourceRoot, err)
	}
	if err := renderHostStorageApplyResult(w, result); err != nil {
		return err
	}
	if strings.TrimSpace(result.TransactionID) == "" {
		return fmt.Errorf("legacy host storage migration did not return a transaction ID; retry with: %s", initLegacyStorageSetCommand())
	}
	finalized, err := finalizeHostStorageAfterReconnect(ctx, client, host, result.TransactionID)
	if err != nil {
		return initLegacyStorageFinalizeError(result.TransactionID, err)
	}
	if !finalized.CleanupPending {
		return fmt.Errorf("legacy host storage transaction %s validated without authorizing cleanup of %s", finalized.TransactionID, candidate.SourceRoot)
	}
	cleanup, err := client.HostStorageCleanup(ctx, catchrpc.HostStorageCleanupRequest{From: candidate.SourceRoot, Yes: true})
	if err != nil {
		return initLegacyStorageCleanupError(candidate.TargetRoot, candidate.SourceRoot, err)
	}
	if err := validateInitLegacyStorageCleanupResult(cleanup, result.TransactionID, candidate.SourceRoot); err != nil {
		return initLegacyStorageCleanupError(candidate.TargetRoot, candidate.SourceRoot, err)
	}
	_, err = fmt.Fprintf(w, "Removed inactive legacy host storage %s (transaction %s).\n", cleanup.Removed, cleanup.TransactionID)
	return err
}

func validateInitLegacyStorageCleanupResult(result catchrpc.HostStorageCleanupResult, transactionID, source string) error {
	gotTransactionID := strings.TrimSpace(result.TransactionID)
	if gotTransactionID == "" {
		return fmt.Errorf("host storage cleanup returned no transaction ID")
	}
	if transactionID != "" && gotTransactionID != transactionID {
		return fmt.Errorf("host storage cleanup returned transaction %q, want %q", gotTransactionID, transactionID)
	}
	if strings.TrimSpace(result.Removed) == "" || !hostSetPathsEqual(result.Removed, source) {
		return fmt.Errorf("host storage cleanup reported removed path %q, want %q", result.Removed, source)
	}
	return nil
}

func initLegacyStorageCleanupError(target, source string, err error) error {
	return fmt.Errorf("new host storage at %s is active, but inactive legacy tree %s still contains sensitive state; resume cleanup with: yeet host cleanup --from=%s --yes: %w", target, source, source, err)
}

func initLegacyStorageFinalizeError(transactionID string, err error) error {
	if initLegacyStorageFinalizeDidNotAnswer(err) {
		return fmt.Errorf("catch could not be reconnected after applying host storage transaction %s; validation and cleanup did not run, and the active storage authority is unknown; retry recovery with: %s: %w", transactionID, initLegacyStorageSetCommand(), err)
	}
	return fmt.Errorf("catch answered Finalize for host storage transaction %s, but finalization did not complete; inspect the Finalize error and any reported rollback result, then retry with: %s: %w", transactionID, initLegacyStorageSetCommand(), err)
}

func initLegacyStorageFinalizeDidNotAnswer(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(err.Error(), "did not reconnect within 60s") ||
		strings.Contains(err.Error(), "wait for Catch")
}

func initLegacyStorageApplyError(source string, err error) error {
	return fmt.Errorf("legacy host storage migration from %s did not complete; inspect the reported rollback state, then retry with: %s: %w", source, initLegacyStorageSetCommand(), err)
}

func initLegacyStorageSetCommand() string {
	return "yeet host set --data-dir=/var/lib/yeet --services-root=/var/lib/yeet/services --migrate-services=all --yes"
}

func canPromptInitStorage() bool {
	return isTerminalFn(int(os.Stdin.Fd())) && isTerminalFn(int(os.Stdout.Fd()))
}

func runInitStorageWizard(in io.Reader, out io.Writer, probe initStorageProbe) (initStorageOptions, error) {
	probe = normalizeInitStorageProbe(probe)
	if _, err := fmt.Fprintln(out, "Storage setup"); err != nil {
		return initStorageOptions{}, err
	}
	return runInitStorageWizardWithPrompter(activePrompter, probe)
}

func runInitStorageWizardWithPrompter(prompter yeetPrompter, probe initStorageProbe) (initStorageOptions, error) {
	storage, err := promptInitDataStorageWithPrompter(prompter, probe)
	if err != nil {
		return initStorageOptions{}, err
	}
	return promptInitServicesStorageWithPrompter(prompter, storage, probe)
}

func promptInitDataStorageWithPrompter(prompter yeetPrompter, probe initStorageProbe) (initStorageOptions, error) {
	storage := initStorageOptions{}
	useDefaultData, err := prompter.Confirm("Use /var/lib/yeet for catch data?", true)
	if err != nil {
		return initStorageOptions{}, err
	}
	if useDefaultData {
		storage.DataDir = defaultInitDataDir
		return storage, nil
	}
	if probe.ZFSAvailable {
		return promptInitCustomDataStorageWithPrompter(prompter, storage, probe, defaultCustomInitDataDir)
	}
	storage.DataDir, err = prompter.Input("Catch data directory", defaultCustomInitDataDir)
	if err != nil {
		return initStorageOptions{}, err
	}
	return storage, nil
}

func promptInitCustomDataStorageWithPrompter(prompter yeetPrompter, storage initStorageOptions, probe initStorageProbe, defaultDataDir string) (initStorageOptions, error) {
	useZFS, err := prompter.Confirm("Use a ZFS dataset for catch data?", true)
	if err != nil {
		return initStorageOptions{}, err
	}
	if useZFS {
		storage.DataDir, err = prompter.Input("Catch data dataset", suggestedInitDataDataset(probe))
		if err != nil {
			return initStorageOptions{}, err
		}
		storage.DataDirZFS = true
		return storage, nil
	}
	storage.DataDir, err = prompter.Input("Catch data directory", defaultDataDir)
	if err != nil {
		return initStorageOptions{}, err
	}
	return storage, nil
}

func promptInitServicesStorageWithPrompter(prompter yeetPrompter, storage initStorageOptions, probe initStorageProbe) (initStorageOptions, error) {
	keepServicesUnderData, err := prompter.Confirm("Keep services under the catch data dir?", true)
	if err != nil {
		return initStorageOptions{}, err
	}
	if keepServicesUnderData {
		return storage, nil
	}
	if !probe.ZFSAvailable {
		storage.ServicesRoot, err = prompter.Input("Services root", suggestedInitServicesRootPath(storage, probe))
		if err != nil {
			return initStorageOptions{}, err
		}
		return storage, nil
	}
	return promptInitCustomServicesStorageWithPrompter(prompter, storage, probe)
}

func promptInitCustomServicesStorageWithPrompter(prompter yeetPrompter, storage initStorageOptions, probe initStorageProbe) (initStorageOptions, error) {
	useZFS, err := prompter.Confirm("Use a ZFS dataset for services?", storage.DataDirZFS)
	if err != nil {
		return initStorageOptions{}, err
	}
	if useZFS {
		storage.ServicesRoot, err = prompter.Input("Services dataset", suggestedInitServicesDataset(storage, probe))
		if err != nil {
			return initStorageOptions{}, err
		}
		storage.ServicesRootZFS = true
		return storage, nil
	}
	storage.ServicesRoot, err = prompter.Input("Services root", suggestedInitServicesRootPath(storage, probe))
	if err != nil {
		return initStorageOptions{}, err
	}
	return storage, nil
}

func normalizeInitStorageProbe(probe initStorageProbe) initStorageProbe {
	probe.Home = strings.TrimSpace(probe.Home)
	if probe.Home == "" {
		probe.Home = "/root"
	}
	probe.SuggestedZFSPrefix = strings.Trim(strings.TrimSpace(probe.SuggestedZFSPrefix), "/")
	return probe
}

func suggestedInitDataDataset(probe initStorageProbe) string {
	if probe.SuggestedZFSPrefix != "" {
		return path.Join(probe.SuggestedZFSPrefix, "data")
	}
	return "flash/yeet/data"
}

func suggestedInitServicesDataset(storage initStorageOptions, probe initStorageProbe) string {
	if storage.DataDirZFS {
		parent := path.Dir(strings.Trim(storage.DataDir, "/"))
		if parent != "." && parent != "/" {
			return path.Join(parent, "services")
		}
	}
	if probe.SuggestedZFSPrefix != "" {
		return path.Join(probe.SuggestedZFSPrefix, "services")
	}
	return "flash/yeet/services"
}

func suggestedInitServicesRootPath(storage initStorageOptions, probe initStorageProbe) string {
	home := probe.Home
	if home == "" {
		home = "/root"
	}
	if !storage.DataDirZFS && strings.TrimSpace(storage.DataDir) != "" {
		parent := filepath.Dir(storage.DataDir)
		if parent != "." && parent != string(filepath.Separator) {
			return filepath.Join(parent, "yeet-services")
		}
	}
	return filepath.Join(home, "yeet-services")
}

func defaultInitStorageHome(bool) string {
	return "/root"
}

func remoteInitExistingCatchStorage(userAtRemote string) (initStorageOptions, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", userAtRemote, "systemctl cat catch.service 2>/dev/null")
	output, err := cmd.Output()
	if err == nil {
		storage := initStorageOptionsFromCatchUnit(string(output))
		if !hostSetPathsEqual(storage.DataDir, defaultInitDataDir) {
			return storage, true, nil
		}
		installHome, found, homeErr := remoteInitRecordedInstallHome(ctx, userAtRemote, storage.DataDir)
		if homeErr != nil {
			return initStorageOptions{}, false, homeErr
		}
		if !found {
			return storage, true, nil
		}
		source, ok := initLegacyStorageSourceFromRecordedHome(installHome)
		if !ok {
			return storage, true, nil
		}
		exists, existsErr := remoteInitLegacySourceExists(ctx, userAtRemote, source)
		if existsErr != nil {
			return initStorageOptions{}, false, existsErr
		}
		if exists {
			storage.legacyCleanupSource = source
		}
		return storage, true, nil
	}
	if ctx.Err() != nil {
		return initStorageOptions{}, false, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return initStorageOptions{}, false, nil
	}
	return initStorageOptions{}, false, err
}

type initRecordedInstallMeta struct {
	InstallHome string `json:"installHome"`
}

const initRemoteInstallMetadataMissingExitCode = 44

func remoteInitRecordedInstallHome(ctx context.Context, userAtRemote, dataDir string) (string, bool, error) {
	dataDir = strings.TrimSpace(dataDir)
	if !filepath.IsAbs(dataDir) {
		return "", false, fmt.Errorf("catch data directory %q is not absolute", dataDir)
	}
	metaPath := filepath.Join(filepath.Clean(dataDir), "install.json")
	remoteCommand := remoteInitPrivilegedReadCommand(metaPath)
	output, err := exec.CommandContext(ctx, "ssh", userAtRemote, remoteCommand).CombinedOutput()
	if ctx.Err() != nil {
		return "", false, ctx.Err()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == initRemoteInstallMetadataMissingExitCode {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read recorded catch install metadata %s on %s: %w", metaPath, userAtRemote, err)
	}
	var meta initRecordedInstallMeta
	if err := json.Unmarshal(output, &meta); err != nil {
		return "", false, fmt.Errorf("decode recorded catch install metadata %s: %w", metaPath, err)
	}
	recordedHome := strings.TrimSpace(meta.InstallHome)
	if recordedHome == "" {
		return "", false, nil
	}
	home := filepath.Clean(recordedHome)
	if !filepath.IsAbs(home) {
		return "", false, fmt.Errorf("recorded catch install home %q in %s is not absolute", meta.InstallHome, metaPath)
	}
	return home, true, nil
}

func remoteInitPrivilegedReadCommand(remotePath string) string {
	quotedPath := shellQuote(remotePath)
	return fmt.Sprintf(
		`if [ "$(id -u)" -eq 0 ]; then if [ ! -e %s ] && [ ! -L %s ]; then exit %d; fi; cat %s; else if sudo -n test ! -e %s && sudo -n test ! -L %s; then exit %d; fi; sudo -n cat %s; fi`,
		quotedPath,
		quotedPath,
		initRemoteInstallMetadataMissingExitCode,
		quotedPath,
		quotedPath,
		quotedPath,
		initRemoteInstallMetadataMissingExitCode,
		quotedPath,
	)
}

func remoteInitLegacySourceExists(ctx context.Context, userAtRemote, source string) (bool, error) {
	if !filepath.IsAbs(source) {
		return false, fmt.Errorf("legacy source %q is not absolute", source)
	}
	remoteCommand := remoteInitPrivilegedPathStatusCommand(source)
	output, err := exec.CommandContext(ctx, "ssh", userAtRemote, remoteCommand).Output()
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		return false, fmt.Errorf("determine whether legacy source %s exists on %s: %w", source, userAtRemote, err)
	}
	switch strings.TrimSpace(string(output)) {
	case "present":
		return true, nil
	case "absent":
		return false, nil
	default:
		return false, fmt.Errorf("determine whether legacy source %s exists on %s: unexpected remote status %q", source, userAtRemote, strings.TrimSpace(string(output)))
	}
}

func remoteInitPrivilegedPathStatusCommand(remotePath string) string {
	quotedPath := shellQuote(remotePath)
	return fmt.Sprintf(
		`if [ "$(id -u)" -eq 0 ]; then if [ -e %s ] || [ -L %s ]; then printf 'present\n'; else printf 'absent\n'; fi; elif sudo -n test -e %s || sudo -n test -L %s; then printf 'present\n'; elif sudo -n test ! -e %s && sudo -n test ! -L %s; then printf 'absent\n'; else exit 45; fi`,
		quotedPath,
		quotedPath,
		quotedPath,
		quotedPath,
		quotedPath,
		quotedPath,
	)
}

func initStorageOptionsFromCatchUnit(unit string) initStorageOptions {
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExecStart=") {
			return initStorageOptionsFromCatchExecStart(strings.Fields(strings.TrimPrefix(line, "ExecStart=")))
		}
	}
	return initStorageOptions{}
}

func initStorageOptionsFromCatchExecStart(args []string) initStorageOptions {
	storage := initStorageOptions{}
	for i := 1; i < len(args); i++ {
		flag, value, next := initStorageCatchExecStartStorageFlag(args, i)
		i = next
		switch flag {
		case "data-dir":
			storage.DataDir = value
		case "services-root":
			storage.ServicesRoot = value
		}
	}
	return storage
}

func initStorageCatchExecStartStorageFlag(args []string, i int) (string, string, int) {
	arg := strings.TrimSpace(args[i])
	name, value, ok := strings.Cut(arg, "=")
	flag := initStorageCatchStorageFlagName(name)
	if ok {
		return flag, value, i
	}
	if flag == "" || i+1 >= len(args) {
		return "", "", i
	}
	return flag, args[i+1], i + 1
}

func initStorageCatchStorageFlagName(name string) string {
	switch strings.TrimSpace(name) {
	case "--data-dir", "-data-dir":
		return "data-dir"
	case "--services-root", "-services-root":
		return "services-root"
	default:
		return ""
	}
}

func remoteInitStorageProbe(userAtRemote string, useSudo bool) (initStorageProbe, error) {
	home := defaultInitStorageHome(useSudo)
	if !useSudo {
		out, err := remoteInitStorageOutputFn(userAtRemote, "printf '%s\\n' \"$HOME\"")
		if err != nil {
			return initStorageProbe{}, err
		}
		if trimmed := strings.TrimSpace(out); trimmed != "" {
			home = trimmed
		}
	}
	probe := initStorageProbe{Home: home}
	if ok, _ := remoteInitStorageCommandOKFn(userAtRemote, "command -v zfs >/dev/null 2>&1"); !ok {
		return probe, nil
	}
	probe.ZFSAvailable = true
	if pool, err := remoteInitStorageOutputFn(userAtRemote, "zfs list -H -d 0 -o name -t filesystem 2>/dev/null | head -n 1"); err == nil {
		if pool = strings.TrimSpace(pool); pool != "" {
			probe.SuggestedZFSPrefix = path.Join(pool, "yeet")
		}
	}
	return probe, nil
}

func remoteInitStorageCommandOK(userAtRemote, script string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", userAtRemote, script)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, err
}

func remoteInitStorageOutput(userAtRemote, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", userAtRemote, script)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	return string(out), nil
}
