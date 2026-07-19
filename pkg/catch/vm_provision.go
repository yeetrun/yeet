// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
	"golang.org/x/sys/unix"
)

var (
	vmProvisionHostProfileFunc        func(*ttyExecer, resolvedServiceRoot, int64) (vmHostProfile, error)
	vmProvisionDiskRunner             vmCommandRunner
	vmProvisionNetworkRunner          vmNetworkCommandRunner
	vmProvisionMetadataInjector       func(context.Context, string, vmMetadataConfig) error
	vmProvisionSSHKeyFunc             func() (string, error)
	vmProvisionSystemdDir             string
	vmProvisionSystemctlFunc          func(args ...string) error
	vmProvisionCommitFunc             func(*ttyExecer, vmProvisionPlan, string, *cli.ServiceSetFlags) error
	vmProvisionGuestReadyBoundaryFunc = captureVMGuestReadyBoundary
	vmProvisionGuestReadyWaitFunc     = waitVMGuestReady
	vmProvisionEnsureRuntimeIdentity  = ensureVMRuntimeIdentity
	ensureVMISOBoundaryForProvision   = func(ctx context.Context, server *Server, service string) error {
		return server.ensureISONetworkBoundaryLocked(ctx, service)
	}
	verifyVMNetworkPlanForProvision      = verifyVMNetworkPlan
	verifyVMISONetworkAbsentForProvision = verifyVMISONetworkPlanAbsent
	acquireVMProvisionLockForProvision   = acquireVMProvisionLock
	prepareHostVMLANBridgeForRunFn       = func(root string) (VMLANBridgePrepareStatus, error) {
		return PrepareVMLANBridge(root, true)
	}
)

type vmProvisionPlan struct {
	Service     string
	ServiceRoot resolvedServiceRoot
	Shape       vmShape
	Balloon     db.VMBalloonConfig
	Image       vmImageAsset
	Disk        vmDiskPlan
	DiskPath    string
	Network     vmNetworkPlan
	SvcNetwork  *db.SvcNetwork
	Metadata    vmMetadataConfig

	FirecrackerConfigPath  string
	FirecrackerConfig      []byte
	SystemdUnitStagePath   string
	SystemdUnitInstallPath string
	SystemdUnitContent     string
	SerialSocket           string
	SerialLog              string
	APISocket              string
	VsockSocket            string
	PIDFile                string
	RuntimeIdentity        vmRuntimeIdentity
}

//nolint:cyclop // Provisioning keeps reservation, rollback, commit, and readiness order explicit.
func (e *ttyExecer) provisionVM(flags cli.RunFlags, payload string) (retErr error) {
	doneProvision := e.traceBlock("vm provision")
	defer doneProvision()
	unlock, err := acquireVMProvisionLockForProvision(e.vmProvisionContext(), e.s.cfg.RootDir, e.sn)
	if err != nil {
		return fmt.Errorf("lock VM provisioning for %q: %w", e.sn, err)
	}
	defer unlock()
	serviceExisted, snapshotPolicyFlags, err := e.validateAndCheckVMProvisionRequest(flags)
	if err != nil {
		return err
	}
	ui := newVMProvisionUI(e)
	ui.Start()
	defer ui.Stop()
	rollbackNewService := !serviceExisted
	var inputs vmProvisionInputs
	defer func() {
		if retErr == nil || !rollbackNewService {
			return
		}
		if err := e.rollbackNewVMProvisionReservation(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("rollback VM service reservation: %w", err))
		}
		if err := e.cleanupFailedNewVMProvisionRoot(inputs.ServiceRoot); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("cleanup failed VM service root: %w", err))
		}
	}()
	ui.StartStep(vmRunStepResolve)
	doneInputs := e.traceBlock("vm inputs")
	inputs, err = e.vmProvisionInputs(flags, payload, ui.ImageProgress())
	doneInputs()
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	doneReserveNetwork := e.traceBlock("vm reserve service network")
	svcNet, err := e.reserveVMServiceNetwork(flags)
	doneReserveNetwork()
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	isoAllocation, err := e.reserveVMISONetwork(flags)
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	donePlan := e.traceBlock("vm plan")
	plan, err := e.newVMProvisionPlan(flags, payload, inputs.ServiceRoot, inputs.Shape, inputs.Image, svcNet, isoAllocation, inputs.SSHKey, inputs.RuntimeIdentity)
	donePlan()
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	ui.DoneStep(vmProvisionPlanDetail(plan))
	doneFinish := e.traceBlock("vm finish")
	committed, _, readinessIncomplete, err := e.finishVMProvision(inputs.Context, plan, payload, flags.Restart, snapshotPolicyFlags, ui)
	rollbackNewService = vmProvisionRollbackPendingAfterFinish(rollbackNewService, committed)
	doneFinish()
	if err != nil {
		printVMProvisionFailureRecovery(ui, plan.Service, committed, readinessIncomplete)
		return err
	}
	rollbackNewService = false
	return nil
}

//nolint:cyclop // Secure open, inode validation, permission checks, and locking are one auditable sequence.
func acquireVMProvisionLock(ctx context.Context, rootDir, service string) (func(), error) {
	if strings.TrimSpace(service) == "" {
		return nil, fmt.Errorf("VM provision lock requires a service name")
	}
	dir, _, err := openValidatedISOOperationRootDir(rootDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	name := "vm-provision-" + isoNameToken(service) + ".lock"
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(dir.Fd()), name, flags, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open VM provision lock %s: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open VM provision lock %s: invalid file descriptor", name)
	}
	stat, err := inspectISOOperationInode(fd)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect VM provision lock %s: %w", name, err)
	}
	if err := validateISOOperationLockStat(name, stat, uint32(os.Geteuid())); err != nil {
		_ = file.Close()
		return nil, err
	}
	if created {
		if err := unix.Fchmod(fd, 0o600); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("secure VM provision lock %s: %w", name, err)
		}
	} else if stat.mode&0o7777 != 0o600 {
		_ = file.Close()
		return nil, fmt.Errorf("existing VM provision lock %s mode is %#o, want 0600", name, stat.mode&0o7777)
	}
	if err := waitForISOOperationLock(ctx, fd); err != nil {
		_ = file.Close()
		return nil, err
	}
	return newISOOperationUnlock(
		func() { _ = unix.Flock(fd, unix.LOCK_UN) },
		func() { _ = file.Close() },
	), nil
}

func printVMProvisionFailureRecovery(ui *vmProvisionUI, service string, committed, readinessIncomplete bool) {
	if committed && readinessIncomplete {
		ui.PrintReadinessFailure(service)
	}
}

func vmProvisionRollbackPendingAfterFinish(rollbackPending, committed bool) bool {
	if committed {
		return false
	}
	return rollbackPending
}

func (e *ttyExecer) validateAndCheckVMProvisionRequest(flags cli.RunFlags) (bool, *cli.ServiceSetFlags, error) {
	serviceExisted, err := e.validateAndCheckVMProvisionService(flags)
	if err != nil {
		return false, nil, err
	}
	snapshotPolicyFlags, err := snapshotFlagsFromRunFlags(flags)
	if err != nil {
		return false, nil, err
	}
	if err := e.ensureVMLANBridgeForRun(flags); err != nil {
		return false, nil, err
	}
	return serviceExisted, snapshotPolicyFlags, nil
}

//nolint:cyclop // Existing incomplete ISO reservations require explicit recovery decisions.
func (e *ttyExecer) validateAndCheckVMProvisionService(flags cli.RunFlags) (bool, error) {
	if err := validateVMProvisionFlags(flags); err != nil {
		return false, err
	}
	doneServiceExists := e.traceBlock("vm service exists")
	serviceExisted, err := e.serviceExists()
	doneServiceExists()
	if err != nil || !serviceExisted {
		return serviceExisted, err
	}
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return true, err
	}
	if sv.ServiceType() == db.ServiceTypeVM {
		vm := sv.VM()
		if vm.Valid() && vm.SetupState() != "ready" && sv.ISO().Valid() && !sv.ISO().RemoveRequested() {
			if !vmNetworkModeRequested(flags.Net, "iso") {
				return true, fmt.Errorf("VM %q has an incomplete ISO provision; retry with --net=iso or remove it", e.sn)
			}
			return false, nil
		}
		return true, fmt.Errorf("VM %q already exists; use yeet vm set for CPU, memory, disk, or network changes, or remove and recreate it", e.sn)
	}
	return true, nil
}

func validateVMProvisionFlags(flags cli.RunFlags) error {
	if _, err := normalizeVMProvisionImagePolicy(flags.ImagePolicy); err != nil {
		return err
	}
	return validateVMNetworkOptions(vmRequestedNetworkModes(flags.Net), flags.MacvlanParent, flags.MacvlanVlan, flags.MacvlanMac)
}

func (e *ttyExecer) ensureVMLANBridgeForRun(flags cli.RunFlags) error {
	modes := vmRequestedNetworkModes(flags.Net)
	if !vmModeListContains(modes, "lan") {
		return nil
	}
	if strings.TrimSpace(flags.MacvlanParent) != "" {
		return nil
	}
	plan, err := resolveHostVMLANBridgeFn()
	if err != nil {
		if plan.NeedsPrepare || errors.Is(err, errVMLANBridgePreparationRequired) {
			return e.prepareVMLANBridgeForRun(plan, err)
		}
		return fmt.Errorf("resolve VM LAN bridge before VM provisioning: %w", err)
	}
	if plan.Ready {
		return nil
	}
	if plan.NeedsPrepare {
		return e.prepareVMLANBridgeForRun(plan, nil)
	}
	return fmt.Errorf("resolve VM LAN bridge before VM provisioning: no usable bridge selected")
}

func (e *ttyExecer) prepareVMLANBridgeForRun(plan vmLANBridgePlan, cause error) error {
	if os.Getenv("CATCH_PREPARE_VM_LAN_BRIDGE") == "1" {
		return e.runVMLANBridgePrepareForRun()
	}
	if e.isPty && e.rw != nil {
		ok, err := e.confirmVMLANBridgePrepareForRun(plan)
		if err != nil {
			return fmt.Errorf("confirm VM LAN bridge preparation before VM provisioning: %w", err)
		}
		if ok {
			return e.runVMLANBridgePrepareForRun()
		}
	}
	return vmLANBridgeProvisionArtifactsError(plan, cause)
}

func (e *ttyExecer) confirmVMLANBridgePrepareForRun(plan vmLANBridgePlan) (bool, error) {
	bridge := strings.TrimSpace(plan.Bridge)
	if bridge == "" {
		bridge = "br0"
	}
	msg := fmt.Sprintf("Prepare %s for VM LAN networking?", bridge)
	if e.bypassPtyInput && e.rawRW != nil {
		return confirmRawLine(e.rawRW, e.rw, msg)
	}
	return cmdutil.Confirm(e.rw, e.rw, msg)
}

func (e *ttyExecer) runVMLANBridgePrepareForRun() error {
	root := ""
	if e.s != nil {
		root = e.s.cfg.RootDir
	}
	status, err := prepareHostVMLANBridgeForRunFn(root)
	if err != nil {
		return fmt.Errorf("prepare VM LAN bridge before VM provisioning: %w", err)
	}
	if status.Phase != string(vmLANBridgePhaseReady) {
		return fmt.Errorf("prepare VM LAN bridge before VM provisioning finished with phase %q: %w", status.Phase, errVMLANBridgePreparationRequired)
	}
	if err := e.vmProvisionContext().Err(); err != nil {
		return fmt.Errorf("VM LAN bridge is ready, but the client disconnected before VM provisioning could continue; rerun `yeet run ... --net=lan`: %w", err)
	}
	return nil
}

func vmLANBridgeProvisionArtifactsError(plan vmLANBridgePlan, cause error) error {
	cause = vmLANBridgePreparationCause(cause)
	bridge := strings.TrimSpace(plan.Bridge)
	parent := strings.TrimSpace(plan.Parent)
	msg := "VM LAN bridge preparation is required before creating VM service artifacts"
	switch {
	case bridge != "" && parent != "":
		return fmt.Errorf("%s for bridge %q from parent %q: %w", msg, bridge, parent, cause)
	case bridge != "":
		return fmt.Errorf("%s for bridge %q: %w", msg, bridge, cause)
	case parent != "":
		return fmt.Errorf("%s from parent %q: %w", msg, parent, cause)
	default:
		return fmt.Errorf("%s: %w", msg, cause)
	}
}

type vmProvisionInputs struct {
	Context         context.Context
	ServiceRoot     resolvedServiceRoot
	Shape           vmShape
	Image           vmImageAsset
	SSHKey          string
	RuntimeIdentity vmRuntimeIdentity
}

func (e *ttyExecer) vmProvisionInputs(flags cli.RunFlags, payload string, ui ProgressUI) (vmProvisionInputs, error) {
	ctx := e.vmProvisionContext()
	inputs := vmProvisionInputs{Context: ctx}
	doneRoot := e.traceBlock("vm prepare service root")
	resolvedRoot, err := e.prepareVMServiceRoot(flags)
	doneRoot()
	if err != nil {
		return inputs, err
	}
	inputs.ServiceRoot = resolvedRoot
	doneShape := e.traceBlock("vm shape")
	shape, err := e.vmProvisionShape(resolvedRoot, flags)
	doneShape()
	if err != nil {
		return inputs, err
	}
	inputs.Shape = shape
	doneSSHKey := e.traceBlock("vm ssh key")
	sshKey, err := e.vmSSHKey()
	doneSSHKey()
	if err != nil {
		return inputs, err
	}
	inputs.SSHKey = sshKey
	doneImage := e.traceBlock("vm image select")
	image, err := e.selectVMProvisionImage(ctx, flags, payload, ui)
	doneImage()
	if err != nil {
		return inputs, err
	}
	inputs.Image = image
	if _, err := image.RequireJailer(); err != nil {
		return inputs, err
	}
	identity, err := vmProvisionEnsureRuntimeIdentity()
	if err != nil {
		return inputs, err
	}
	inputs.RuntimeIdentity = identity
	return inputs, nil
}

func (e *ttyExecer) vmProvisionContext() context.Context {
	if e.ctx != nil {
		return e.ctx
	}
	return context.Background()
}

func (e *ttyExecer) prepareVMServiceRoot(flags cli.RunFlags) (resolvedServiceRoot, error) {
	resolvedRoot, err := e.s.prepareServiceRootForInstall(e.sn, flags.ServiceRoot, flags.ZFS)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	if !resolvedRoot.ZFS {
		_, err := os.Stat(resolvedRoot.Root)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return resolvedServiceRoot{}, fmt.Errorf("stat VM service root: %w", err)
			}
			resolvedRoot.Created = true
		}
	}
	if err := ensureDirsForRoot(resolvedRoot.Root, e.user); err != nil {
		return resolvedServiceRoot{}, fmt.Errorf("prepare VM service root: %w", err)
	}
	return resolvedRoot, nil
}

func (e *ttyExecer) vmProvisionShape(resolvedRoot resolvedServiceRoot, flags cli.RunFlags) (vmShape, error) {
	runningVMBytes, runningVMMinBytes, err := e.runningVMMemory()
	if err != nil {
		return vmShape{}, err
	}
	profile, err := e.vmHostProfile(resolvedRoot, runningVMBytes)
	if err != nil {
		return vmShape{}, err
	}
	if runningVMMinBytes > 0 && profile.RunningVMMinBytes == 0 {
		profile.RunningVMMinBytes = runningVMMinBytes
	}
	policy, err := e.s.vmHostMemoryPolicy()
	if err != nil {
		return vmShape{}, err
	}
	return vmShapeFromRunFlags(profile, flags, policy)
}

func (e *ttyExecer) selectVMProvisionImage(ctx context.Context, flags cli.RunFlags, payload string, ui ProgressUI) (vmImageAsset, error) {
	donePolicy := e.traceBlock("vm image policy")
	policy, err := normalizeVMProvisionImagePolicy(flags.ImagePolicy)
	donePolicy()
	if err != nil {
		return vmImageAsset{}, err
	}
	cache := e.vmImageCache()
	doneInspect := e.traceBlock("vm image inspect")
	state, latestManifest, err := vmImageInspectFunc(ctx, cache, payload)
	doneInspect()
	if err != nil {
		return vmImageAsset{}, err
	}
	e.tracef("vm image cache state=%s cached=%s latest=%s", state.State, state.CachedVersion, vmLatestVersionForMessage(state, latestManifest))
	switch state.State {
	case vmImageCacheMissing:
		done := e.traceBlock("vm image ensure missing")
		asset, err := e.ensureManagedVMImageAndPrune(ctx, cache, payload, ui)
		done()
		return asset, err
	case vmImageCacheCurrent:
		done := e.traceBlock("vm image cached asset")
		asset, err := currentVMImageAsset(ctx, cache, payload, state)
		done()
		return asset, err
	case vmImageCacheStale:
		return e.selectStaleVMProvisionImage(ctx, cache, payload, policy, state, latestManifest, ui)
	default:
		return vmImageAsset{}, fmt.Errorf("unknown VM image cache state %q for %s", state.State, payload)
	}
}

func currentVMImageAsset(ctx context.Context, cache vmImageCache, payload string, state vmImageCacheState) (vmImageAsset, error) {
	if strings.TrimSpace(state.ManifestURL) != "" {
		return cachedVMImageAsset(ctx, cache.withManifestURL(state.ManifestURL), state.CachedVersion)
	}
	source, err := cache.resolveVMImagePayload(ctx, payload)
	if err != nil {
		return vmImageAsset{}, err
	}
	if source.Kind == vmImageSourceLocal {
		return resolveLocalVMImageAssetForPayload(ctx, cache.Root, source.LocalName)
	}
	return cachedVMImageAsset(ctx, cache.withManifestURL(source.ManifestURL), state.CachedVersion)
}

func normalizeVMProvisionImagePolicy(policy string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", "prompt":
		return "prompt", nil
	case "update":
		return "update", nil
	case "cached":
		return "cached", nil
	default:
		return "", fmt.Errorf("--image-policy must be prompt, update, or cached")
	}
}

func (e *ttyExecer) selectStaleVMProvisionImage(ctx context.Context, cache vmImageCache, payload, policy string, state vmImageCacheState, latestManifest vmImageManifest, ui ProgressUI) (vmImageAsset, error) {
	switch policy {
	case "update":
		done := e.traceBlock("vm image ensure stale")
		asset, err := e.ensureManagedVMImageAndPrune(ctx, cache, payload, ui)
		done()
		return asset, err
	case "cached":
		done := e.traceBlock("vm image cached stale")
		asset, err := cachedVMImageAsset(ctx, cache, state.CachedVersion)
		done()
		return asset, err
	case "prompt":
		if !e.isPty || e.rw == nil {
			return vmImageAsset{}, staleVMImagePolicyError(payload, state, latestManifest)
		}
		finishPrompt := pauseVMImageProgressForPrompt(ui)
		donePrompt := e.traceBlock("vm image stale prompt")
		update, err := e.confirmStaleVMImageUpdate(payload, state, latestManifest)
		finishPrompt()
		donePrompt()
		if err != nil {
			return vmImageAsset{}, err
		}
		if update {
			done := e.traceBlock("vm image ensure prompt")
			asset, err := e.ensureManagedVMImageAndPrune(ctx, cache, payload, ui)
			done()
			return asset, err
		}
		done := e.traceBlock("vm image cached prompt")
		asset, err := cachedVMImageAsset(ctx, cache, state.CachedVersion)
		done()
		return asset, err
	default:
		return vmImageAsset{}, fmt.Errorf("--image-policy must be prompt, update, or cached")
	}
}

type vmImagePromptProgressUI interface {
	PauseForPrompt() func()
}

func pauseVMImageProgressForPrompt(ui ProgressUI) func() {
	if promptUI, ok := ui.(vmImagePromptProgressUI); ok {
		return promptUI.PauseForPrompt()
	}
	return func() {}
}

func (e *ttyExecer) confirmStaleVMImageUpdate(payload string, state vmImageCacheState, latestManifest vmImageManifest) (bool, error) {
	msg := staleVMImagePrompt(payload, state, latestManifest)
	if e.bypassPtyInput && e.rawRW != nil {
		return confirmRawLine(e.rawRW, e.rw, msg)
	}
	return cmdutil.Confirm(e.rw, e.rw, msg)
}

func confirmRawLine(r io.Reader, w io.Writer, msg string) (bool, error) {
	if _, err := fmt.Fprintf(w, "%s [y/N]: ", msg); err != nil {
		return false, fmt.Errorf("failed to write confirmation prompt: %w", err)
	}
	answer, err := readRawConfirmationAnswer(r, w)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(string(answer)), "y"), nil
}

func readRawConfirmationAnswer(r io.Reader, w io.Writer) ([]byte, error) {
	var answer []byte
	for {
		b, err := readRawConfirmationByte(r)
		if err != nil {
			return nil, err
		}
		done, err := handleRawConfirmationByte(&answer, b, w)
		if err != nil {
			return nil, err
		}
		if done {
			return answer, nil
		}
	}
}

func readRawConfirmationByte(r io.Reader) (byte, error) {
	var b [1]byte
	for {
		n, err := r.Read(b[:])
		if n > 0 {
			return b[0], nil
		}
		if err != nil {
			return 0, fmt.Errorf("failed to read confirmation: %w", err)
		}
	}
}

func handleRawConfirmationByte(answer *[]byte, b byte, w io.Writer) (bool, error) {
	switch b {
	case '\r', '\n':
		return true, writeRawConfirmationEcho(w, "\n")
	case 0x03:
		return false, rawConfirmationInterrupt(w, "^C\n", "interrupted")
	case 0x1c:
		return false, rawConfirmationInterrupt(w, "^\\\n", "quit")
	case '\b', 0x7f:
		return false, rawConfirmationBackspace(answer, w)
	default:
		*answer = append(*answer, b)
		return false, writeRawConfirmationEchoBytes(w, []byte{b})
	}
}

func rawConfirmationInterrupt(w io.Writer, echo, msg string) error {
	if err := writeRawConfirmationEcho(w, echo); err != nil {
		return err
	}
	return errors.New(msg)
}

func rawConfirmationBackspace(answer *[]byte, w io.Writer) error {
	if len(*answer) == 0 {
		return nil
	}
	*answer = (*answer)[:len(*answer)-1]
	return writeRawConfirmationEcho(w, "\b \b")
}

func writeRawConfirmationEcho(w io.Writer, s string) error {
	return writeRawConfirmationEchoBytes(w, []byte(s))
}

func writeRawConfirmationEchoBytes(w io.Writer, b []byte) error {
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("failed to write confirmation echo: %w", err)
	}
	return nil
}

func staleVMImagePrompt(payload string, state vmImageCacheState, latestManifest vmImageManifest) string {
	return fmt.Sprintf("Update VM image %s (cached %s, latest %s)?", payload, vmImagePromptVersion(state.CachedVersion), vmImagePromptVersion(vmLatestVersionForMessage(state, latestManifest)))
}

func staleVMImagePolicyError(payload string, state vmImageCacheState, latestManifest vmImageManifest) error {
	return fmt.Errorf("VM image cache for %s is stale: cached version %s, latest version %s; rerun with --image-policy=update to download the latest image or --image-policy=cached to use the cached image (or run yeet vm images update)", payload, vmImageVersionForMessage(state.CachedVersion), vmImageVersionForMessage(vmLatestVersionForMessage(state, latestManifest)))
}

func vmLatestVersionForMessage(state vmImageCacheState, latestManifest vmImageManifest) string {
	if strings.TrimSpace(state.LatestVersion) != "" {
		return state.LatestVersion
	}
	return latestManifest.Version
}

func vmImagePromptVersion(version string) string {
	version = vmImageVersionForMessage(version)
	idx := strings.LastIndex(version, "-v")
	if idx < 0 {
		return version
	}
	suffix := version[idx+1:]
	if !isNumericVersionSuffix(suffix) {
		return version
	}
	return suffix
}

func isNumericVersionSuffix(suffix string) bool {
	if len(suffix) < 2 || suffix[0] != 'v' {
		return false
	}
	for _, r := range suffix[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func vmImageVersionForMessage(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "unknown"
	}
	return version
}

func cachedVMImageAsset(ctx context.Context, cache vmImageCache, version string) (vmImageAsset, error) {
	root := strings.TrimSpace(cache.Root)
	if root == "" {
		return vmImageAsset{}, fmt.Errorf("VM image cache root is required")
	}
	version = strings.TrimSpace(version)
	if err := validateVMImageCacheDirName(version); err != nil {
		return vmImageAsset{}, err
	}
	dir := filepath.Join(root, version)
	manifest, ok, err := readCachedVMImageManifest(dir)
	if err != nil {
		return vmImageAsset{}, err
	}
	if !ok {
		return vmImageAsset{}, fmt.Errorf("cached VM image %s is not available; run yeet vm images update or rerun with --image-policy=update", version)
	}
	if manifest.Version != version {
		return vmImageAsset{}, fmt.Errorf("cached VM image manifest version %q does not match cache version %q", manifest.Version, version)
	}
	if !cachedVMImageArtifactsReady(dir, manifest) {
		return vmImageAsset{}, fmt.Errorf("cached VM image %s is incomplete; run yeet vm images update or rerun with --image-policy=update", version)
	}
	paths := cachedVMImagePaths(dir, manifest)
	if err := normalizeVMImageRuntimePermissions(paths); err != nil {
		return vmImageAsset{}, err
	}
	preparedRootFS, err := prepareVMRootFSFunc(ctx, paths.RootFSPath)
	if err != nil {
		return vmImageAsset{}, err
	}
	return vmImageAsset{Paths: paths, PreparedRootFSPath: preparedRootFS, Manifest: manifest}, nil
}

func cachedVMImagePaths(dir string, manifest vmImageManifest) vmImagePaths {
	paths := vmImagePaths{
		Manifest:        filepath.Join(dir, "manifest.json"),
		Dir:             dir,
		KernelPath:      filepath.Join(dir, manifest.Kernel),
		RootFSPath:      filepath.Join(dir, manifest.RootFS),
		FirecrackerPath: filepath.Join(dir, manifest.Firecracker),
	}
	if strings.TrimSpace(manifest.Jailer) != "" {
		paths.JailerPath = filepath.Join(dir, manifest.Jailer)
	}
	if strings.TrimSpace(manifest.Initrd) != "" {
		paths.InitrdPath = filepath.Join(dir, manifest.Initrd)
	}
	return paths
}

func (e *ttyExecer) finishVMProvision(ctx context.Context, plan vmProvisionPlan, payload string, restart bool, snapshotPolicyFlags *cli.ServiceSetFlags, ui *vmProvisionUI) (committed bool, ready vmGuestReadyReport, readinessIncomplete bool, retErr error) {
	networkTouched := false
	systemdTouched := false
	defer func() {
		retErr = e.rollbackFailedVMProvisionFinish(ctx, retErr, plan, committed, systemdTouched, networkTouched)
	}()

	doneArtifacts := e.traceBlock("vm artifacts")
	touched, err := e.applyVMProvisionArtifacts(ctx, plan, ui)
	networkTouched = touched
	if err != nil {
		doneArtifacts()
		return committed, ready, false, err
	}
	doneArtifacts()
	doneInstall := e.traceBlock("vm install systemd")
	systemdTouched, err = e.installVMSystemdUnit(plan, ui)
	if err != nil {
		doneInstall()
		return committed, ready, false, err
	}
	doneInstall()
	ui.StartStep(vmRunStepCommit)
	doneCommit := e.traceBlock("vm commit")
	var commitWarning error
	if err := e.commitVMProvisionForFinish(plan, payload, snapshotPolicyFlags); err != nil {
		doneCommit()
		ui.FailStep(err.Error())
		if !dbMutationCommitted(err) {
			return committed, ready, false, err
		}
		committed = true
		commitWarning = err
	} else {
		doneCommit()
		ui.DoneStep("")
		committed = true
	}
	if restart {
		doneStart := e.traceBlock("vm start")
		ready, err = e.startVMAfterProvision(ctx, plan, ui)
		if err != nil {
			doneStart()
			return committed, ready, true, errors.Join(commitWarning, err)
		}
		doneStart()
	}
	if commitWarning != nil {
		return committed, ready, false, commitWarning
	}
	ui.PrintSuccess(plan, payload, ready, restart)
	return committed, ready, false, nil
}

func (e *ttyExecer) rollbackFailedVMProvisionFinish(ctx context.Context, retErr error, plan vmProvisionPlan, committed, systemdTouched, networkTouched bool) error {
	if retErr == nil || committed {
		return retErr
	}
	if systemdTouched {
		if err := cleanupFailedVMSystemdUnit(plan); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("cleanup failed VM systemd unit: %w", err))
		}
	}
	if networkTouched {
		cleanupErr := plan.Network.ExecuteCleanup(vmProvisionNetworkRunner)
		if plan.Network.hasNetworkMode("iso") {
			cleanupErr = errors.Join(cleanupErr, verifyVMISONetworkAbsentForProvision(ctx, plan.Network))
			if cleanupErr != nil {
				cleanupErr = errors.Join(cleanupErr, e.retainFailedVMProvisionISOTombstone(cleanupErr))
			}
		}
		if cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("cleanup failed VM network: %w", cleanupErr))
		}
	}
	return retErr
}

func (e *ttyExecer) commitVMProvisionForFinish(plan vmProvisionPlan, payload string, snapshotPolicyFlags *cli.ServiceSetFlags) error {
	if vmProvisionCommitFunc != nil {
		return vmProvisionCommitFunc(e, plan, payload, snapshotPolicyFlags)
	}
	return e.commitVMProvision(plan, payload, snapshotPolicyFlags)
}

func cleanupFailedVMSystemdUnit(plan vmProvisionPlan) error {
	systemctl := vmProvisionSystemctlFunc
	if systemctl == nil {
		systemctl = runVMSystemctl
	}
	unit := filepath.Base(plan.SystemdUnitInstallPath)
	var cleanupErr error
	if err := systemctl("disable", unit); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("disable VM systemd unit: %w", err))
	}
	if err := os.Remove(plan.SystemdUnitInstallPath); err != nil && !os.IsNotExist(err) {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove VM systemd unit: %w", err))
	}
	if err := systemctl("daemon-reload"); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("reload systemd after VM unit cleanup: %w", err))
	}
	return cleanupErr
}

func (e *ttyExecer) startVMAfterProvision(ctx context.Context, plan vmProvisionPlan, ui *vmProvisionUI) (vmGuestReadyReport, error) {
	ui.StartStep(vmRunStepStart)
	captureBoundary := vmProvisionGuestReadyBoundaryFunc
	if captureBoundary == nil {
		captureBoundary = captureVMGuestReadyBoundary
	}
	doneBoundary := e.traceBlock("vm readiness boundary")
	readyBoundary, err := captureBoundary(ctx, plan.Service)
	doneBoundary()
	if err != nil {
		ui.FailStep(err.Error())
		return vmGuestReadyReport{}, err
	}
	doneRestart := e.traceBlock("vm systemd restart")
	if err := e.restartVMSystemdUnit(plan); err != nil {
		doneRestart()
		ui.FailStep(err.Error())
		return vmGuestReadyReport{}, err
	}
	doneRestart()
	ui.DoneStep("")
	ui.StartStep(vmRunStepReadiness)
	waitReady := vmProvisionGuestReadyWaitFunc
	if waitReady == nil {
		waitReady = waitVMGuestReady
	}
	doneWait := e.traceBlock("vm guest readiness wait")
	report, err := waitReady(ctx, vmGuestReadyWaitInput{
		Service:     plan.Service,
		Network:     plan.Network,
		Boundary:    readyBoundary,
		VsockSocket: plan.VsockSocket,
	})
	doneWait()
	if err != nil {
		ui.FailStep(err.Error())
		return report, err
	}
	detail := ""
	if report.IP.IsValid() {
		detail = report.IP.String()
	}
	ui.DoneStep(detail)
	return report, nil
}

func (e *ttyExecer) serviceExists() (bool, error) {
	dv, err := e.s.getDB()
	if err != nil {
		return false, err
	}
	_, ok := dv.Services().GetOk(e.sn)
	return ok, nil
}

func (e *ttyExecer) rollbackNewVMProvisionReservation() error {
	_, err := e.s.cfg.DB.MutateData(func(d *db.Data) error {
		s := d.Services[e.sn]
		if s == nil {
			return nil
		}
		if s.VM != nil && s.VM.SetupState == "ready" {
			return nil
		}
		if s.ISO != nil && s.ISO.RemoveRequested {
			return nil
		}
		// Preserve an incomplete ISO VM reservation so a retry retains the same
		// /30. The minimal VM record also makes crash recovery and removal
		// type-safe before the complete provision commit exists.
		if s.ISO != nil && s.ISO.Kind == string(iso.PayloadVM) {
			return nil
		}
		delete(d.Services, e.sn)
		return nil
	})
	return err
}

func (e *ttyExecer) cleanupFailedNewVMProvisionRoot(root resolvedServiceRoot) error {
	if strings.TrimSpace(root.Root) == "" {
		return nil
	}
	if root.ZFS && root.Dataset != "" {
		if !root.Created {
			return nil
		}
		return e.s.destroyServiceRootZFS(root.Dataset)
	}
	return cleanupFailedVMFilesystemRoot(root.Root, root.Created)
}

func cleanupFailedVMFilesystemRoot(root string, removeRoot bool) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read failed VM service root %q: %w", root, err)
	}
	var errs []error
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("remove failed VM service root child %q: %w", path, err))
		}
	}
	if removeRoot {
		if err := os.Remove(root); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove failed VM service root %q: %w", root, err))
		}
	}
	return errors.Join(errs...)
}

func (e *ttyExecer) vmSSHKey() (string, error) {
	if key, ok := normalizeVMAuthorizedKeyLine(e.vmSSHAuthorizedKey); ok {
		return key, nil
	}
	if strings.TrimSpace(e.vmSSHAuthorizedKey) != "" {
		return "", fmt.Errorf("invalid VM SSH public key from client")
	}
	keyFunc := vmProvisionSSHKeyFunc
	if keyFunc == nil {
		keyFunc = defaultVMSSHKey
	}
	key, err := keyFunc()
	if err != nil {
		return "", fmt.Errorf("select VM SSH key: %w", err)
	}
	return key, nil
}

func (e *ttyExecer) vmHostProfile(resolvedRoot resolvedServiceRoot, runningVMBytes int64) (vmHostProfile, error) {
	if vmProvisionHostProfileFunc != nil {
		return vmProvisionHostProfileFunc(e, resolvedRoot, runningVMBytes)
	}
	return localVMHostProfile(availableStorageBytes(resolvedRoot.Root), resolvedRoot.ZFS, runningVMBytes), nil
}

func vmShapeFromRunFlags(profile vmHostProfile, flags cli.RunFlags, policy vmMemoryPolicy) (vmShape, error) {
	shape, err := defaultVMShape(profile)
	if err != nil {
		return vmShape{}, err
	}
	if err := applyVMShapeOverrides(&shape, flags); err != nil {
		return vmShape{}, err
	}
	if err := applyVMBalloonOverrides(&shape, flags); err != nil {
		return vmShape{}, err
	}
	if err := validateVMShape(shape); err != nil {
		return vmShape{}, err
	}
	if err := admitVMMemory(profile, shape.MemoryBytes, shape.MinMemoryBytes, policy); err != nil {
		return vmShape{}, err
	}
	return shape, nil
}

func applyVMShapeOverrides(shape *vmShape, flags cli.RunFlags) error {
	if flags.CPUs < 0 {
		return fmt.Errorf("VM CPU count must be positive")
	}
	if flags.CPUs > 0 {
		shape.CPUs = flags.CPUs
	}
	if err := applyVMSizeOverride(&shape.MemoryBytes, flags.Memory); err != nil {
		return err
	}
	return applyVMSizeOverride(&shape.DiskBytes, flags.Disk)
}

func applyVMBalloonOverrides(shape *vmShape, flags cli.RunFlags) error {
	if mode := strings.TrimSpace(flags.Balloon); mode != "" {
		normalized, err := normalizeVMBalloonMode(mode)
		if err != nil {
			return err
		}
		shape.BalloonMode = normalized
	}
	if value := strings.TrimSpace(flags.MemoryMin); value != "" {
		minBytes, err := parseVMSize(value)
		if err != nil {
			return fmt.Errorf("invalid --memory-min: %w", err)
		}
		shape.MinMemoryBytes = minBytes
	}
	cfg, err := effectiveVMBalloonConfig(shape.MemoryBytes, db.VMBalloonConfig{Mode: shape.BalloonMode, MinBytes: shape.MinMemoryBytes})
	if err != nil {
		return err
	}
	shape.BalloonMode = cfg.Mode
	shape.MinMemoryBytes = cfg.MinBytes
	return nil
}

func firecrackerBalloonFromConfig(cfg db.VMBalloonConfig) *firecrackerBalloon {
	if cfg.Mode != vmBalloonModeAuto {
		return nil
	}
	return &firecrackerBalloon{
		AmountMib:             0,
		DeflateOnOOM:          true,
		StatsPollingIntervalS: cfg.StatsIntervalSeconds,
	}
}

func applyVMSizeOverride(dst *int64, raw string) error {
	bytes, err := parseVMSize(raw)
	if err != nil {
		return err
	}
	if bytes > 0 {
		*dst = bytes
	}
	return nil
}

func validateVMShape(shape vmShape) error {
	switch {
	case shape.CPUs <= 0:
		return fmt.Errorf("VM CPU count must be positive")
	case shape.MemoryBytes <= 0:
		return fmt.Errorf("VM memory must be positive")
	case shape.MinMemoryBytes < 0:
		return fmt.Errorf("VM minimum memory must not be negative")
	case shape.MinMemoryBytes > shape.MemoryBytes:
		return fmt.Errorf("VM minimum memory %s exceeds maximum memory %s", formatBytesInt(shape.MinMemoryBytes), formatBytesInt(shape.MemoryBytes))
	case shape.DiskBytes <= 0:
		return fmt.Errorf("VM disk size must be positive")
	default:
		return nil
	}
}

func vmGuestUserForImage(payload string, manifest vmImageManifest) string {
	return manifest.DefaultUserOr("ubuntu")
}

func vmMetadataDriverForImage(payload string, manifest vmImageManifest) string {
	return manifest.MetadataDriverOr("ubuntu")
}

func (e *ttyExecer) newVMProvisionPlan(flags cli.RunFlags, payload string, resolvedRoot resolvedServiceRoot, shape vmShape, image vmImageAsset, svcNet *db.SvcNetwork, isoAllocation *db.ISOAllocation, sshKey string, runtimeIdentity vmRuntimeIdentity) (vmProvisionPlan, error) {
	networkPlan, err := e.vmNetworkPlanFromFlags(flags, svcNet, isoAllocation)
	if err != nil {
		return vmProvisionPlan{}, err
	}
	networkPlan, jailerPath, err := prepareVMProvisionRuntime(image, runtimeIdentity, networkPlan)
	if err != nil {
		return vmProvisionPlan{}, err
	}
	guestUser := vmGuestUserForImage(payload, image.Manifest)
	metadataDriver := vmMetadataDriverForImage(payload, image.Manifest)

	runDir := serviceRunDirForRoot(resolvedRoot.Root)
	binDir := serviceBinDirForRoot(resolvedRoot.Root)
	diskPlan := vmDiskPlan{
		Service:    e.sn,
		Backend:    shape.DiskBackend,
		Path:       filepath.Join(serviceDataDirForRoot(resolvedRoot.Root), "rootfs.raw"),
		Bytes:      shape.DiskBytes,
		BaseRootFS: image.DiskRootFSPath(),
		BaseBytes:  image.Manifest.RootFSSize,
	}
	if shape.DiskBackend == vmDiskBackendZVOL {
		baseDataset := vmZVOLBaseDataset(resolvedRoot, image.Manifest.Version)
		diskPlan.Path = vmZVOLRootDataset(resolvedRoot, e.sn)
		diskPlan.BaseDataset = baseDataset
		diskPlan.ImageVersion = image.Manifest.Version
	}
	diskPath := vmDiskPathForRuntime(diskPlan)

	firecrackerPath := filepath.Join(runDir, "firecracker.json")
	apiSocket := filepath.Join(runDir, "firecracker.sock")
	vsockSocket := filepath.Join(runDir, "vsock.sock")
	unitName := vmSystemdUnitName(e.sn)
	systemdDir := vmProvisionSystemdDir
	if systemdDir == "" {
		systemdDir = vmSystemdSystemDir
	}

	fastBoot := vmImageSupportsFastBoot(image.Manifest)
	bootArgs := vmLegacyKernelBootArgs
	if fastBoot {
		bootArgs, err = vmKernelBootArgs(e.sn, networkPlan, image.Manifest)
		if err != nil {
			return vmProvisionPlan{}, err
		}
	}
	balloonConfig, err := effectiveVMBalloonConfig(shape.MemoryBytes, db.VMBalloonConfig{
		Mode:     shape.BalloonMode,
		MinBytes: shape.MinMemoryBytes,
	})
	if err != nil {
		return vmProvisionPlan{}, err
	}
	firecrackerConfig, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource: firecrackerBootSource{
			KernelImagePath: image.Paths.KernelPath,
			InitrdPath:      image.Paths.InitrdPath,
			BootArgs:        bootArgs,
		},
		Drives: []firecrackerDrive{{
			DriveID:      "rootfs",
			PathOnHost:   diskPath,
			IsRootDevice: true,
			IsReadOnly:   false,
		}},
		NetworkInterfaces: networkPlan.FirecrackerInterfaces(),
		MachineConfig: firecrackerMachineConfig{
			VCPUCount:  shape.CPUs,
			MemSizeMib: int(shape.MemoryBytes >> 20),
		},
		Vsock: &firecrackerVsock{
			VsockID:  vmAgentVsockID,
			GuestCID: vmAgentGuestCID,
			UDSPath:  vsockSocket,
		},
		Balloon: firecrackerBalloonFromConfig(balloonConfig),
	})
	if err != nil {
		return vmProvisionPlan{}, err
	}
	unit, err := renderVMSystemdUnit(vmSystemdConfig{
		Service:          e.sn,
		Runner:           e.s.catchRunnerPath(),
		DataDir:          e.s.cfg.RootDir,
		ServicesRoot:     e.s.cfg.ServicesRoot,
		ServiceRoot:      resolvedRoot.Root,
		DiskPath:         diskPath,
		Firecracker:      image.Paths.FirecrackerPath,
		Jailer:           jailerPath,
		JailerBase:       vmJailerBaseForDataRoot(e.s.cfg.RootDir),
		ConfigPath:       firecrackerPath,
		APISocket:        apiSocket,
		ConsoleSocket:    filepath.Join(runDir, "serial.sock"),
		VsockSocket:      vsockSocket,
		WorkingDirectory: resolvedRoot.Root,
	})
	if err != nil {
		return vmProvisionPlan{}, err
	}

	return vmProvisionPlan{
		Service:                e.sn,
		ServiceRoot:            resolvedRoot,
		Shape:                  shape,
		Balloon:                balloonConfig,
		Image:                  image,
		Disk:                   diskPlan,
		DiskPath:               diskPath,
		Network:                networkPlan,
		SvcNetwork:             svcNet,
		Metadata:               vmMetadataConfig{Hostname: e.sn, User: guestUser, SSHKey: sshKey, Networks: networkPlan.MetadataNetworks(), FastBoot: fastBoot, MetadataDriver: metadataDriver, HostKeyDir: filepath.Join(resolvedRoot.Root, "metadata", "ssh-host-keys")},
		FirecrackerConfigPath:  firecrackerPath,
		FirecrackerConfig:      firecrackerConfig,
		SystemdUnitStagePath:   filepath.Join(binDir, unitName),
		SystemdUnitInstallPath: filepath.Join(systemdDir, unitName),
		SystemdUnitContent:     unit,
		SerialSocket:           filepath.Join(runDir, "serial.sock"),
		SerialLog:              filepath.Join(runDir, "serial.log"),
		APISocket:              apiSocket,
		VsockSocket:            vsockSocket,
		PIDFile:                filepath.Join(runDir, "firecracker.pid"),
		RuntimeIdentity:        runtimeIdentity,
	}, nil
}

func prepareVMProvisionRuntime(image vmImageAsset, identity vmRuntimeIdentity, network vmNetworkPlan) (vmNetworkPlan, string, error) {
	if identity.UID <= 0 || identity.GID <= 0 {
		return vmNetworkPlan{}, "", fmt.Errorf("VM runtime identity must be non-root")
	}
	jailerPath, err := image.RequireJailer()
	if err != nil {
		return vmNetworkPlan{}, "", err
	}
	return network.WithTapOwner(identity), jailerPath, nil
}

func (e *ttyExecer) applyVMProvisionArtifacts(ctx context.Context, plan vmProvisionPlan, ui *vmProvisionUI) (networkTouched bool, err error) {
	ui.StartStep(vmRunStepDisk)
	doneDisk := e.traceBlock("vm disk provision")
	if plan.Disk.Backend == vmDiskBackendZVOL {
		if err := runVMProvisionDiskPlanWithProgress(ctx, plan.Disk, vmProvisionDiskRunner, ui.UpdateDetail); err != nil {
			doneDisk()
			ui.FailStep(err.Error())
			return networkTouched, err
		}
	} else {
		if err := runVMProvisionDiskPlan(ctx, plan.Disk, vmProvisionDiskRunner); err != nil {
			doneDisk()
			ui.FailStep(err.Error())
			return networkTouched, err
		}
	}
	doneDisk()
	ui.DoneStep(formatVMProvisionBytes(plan.Shape.DiskBytes))
	ui.StartStep(vmRunStepMetadata)
	doneWriteMetadata := e.traceBlock("vm write metadata")
	if err := writeVMMetadata(plan.ServiceRoot.Root, plan.Metadata); err != nil {
		doneWriteMetadata()
		retErr := fmt.Errorf("write VM metadata: %w", err)
		ui.FailStep(retErr.Error())
		return networkTouched, retErr
	}
	doneWriteMetadata()
	injectMetadata := vmProvisionMetadataInjector
	if injectMetadata == nil {
		injectMetadata = injectVMMetadataIntoRootFS
	}
	doneInject := e.traceBlock("vm inject metadata")
	if err := injectMetadata(ctx, plan.DiskPath, plan.Metadata); err != nil {
		doneInject()
		retErr := fmt.Errorf("inject VM metadata: %w", err)
		ui.FailStep(retErr.Error())
		return networkTouched, retErr
	}
	doneInject()
	ui.DoneStep("")
	ui.StartStep(vmRunStepConfig)
	doneConfig := e.traceBlock("vm firecracker config")
	if err := writeVMProvisionConfigArtifacts(plan); err != nil {
		doneConfig()
		ui.FailStep(err.Error())
		return networkTouched, err
	}
	doneConfig()
	ui.DoneStep("")
	ui.StartStep(vmRunStepNetwork)
	doneNetwork := e.traceBlock("vm network setup")
	networkTouched = true
	if err := e.applyVMProvisionNetwork(ctx, plan.Network); err != nil {
		doneNetwork()
		retErr := fmt.Errorf("set up VM network: %w", err)
		ui.FailStep(retErr.Error())
		return networkTouched, retErr
	}
	doneNetwork()
	ui.DoneStep(formatVMProvisionNetwork(plan.Network))
	ui.StartStep(vmRunStepStage)
	doneUnit := e.traceBlock("vm stage systemd unit")
	if err := writeVMFile(plan.SystemdUnitStagePath, []byte(plan.SystemdUnitContent), 0o644); err != nil {
		doneUnit()
		retErr := fmt.Errorf("stage VM systemd unit: %w", err)
		ui.FailStep(retErr.Error())
		return networkTouched, retErr
	}
	doneUnit()
	ui.DoneStep("")
	return networkTouched, nil
}

func (e *ttyExecer) applyVMProvisionNetwork(ctx context.Context, plan vmNetworkPlan) error {
	if !plan.hasNetworkMode("iso") {
		return plan.ExecuteSetup(vmProvisionNetworkRunner)
	}
	return e.s.withISOOperationLock(ctx, func() error {
		if err := ensureVMISOBoundaryForProvision(ctx, e.s, e.sn); err != nil {
			return err
		}
		if err := plan.ExecuteSetup(vmProvisionNetworkRunner); err != nil {
			return e.failVMProvisionISOAttachment(ctx, plan, fmt.Errorf("set up VM ISO TAP: %w", err))
		}
		if err := verifyVMNetworkPlanForProvision(ctx, plan); err != nil {
			return e.failVMProvisionISOAttachment(ctx, plan, fmt.Errorf("verify VM ISO TAP: %w", err))
		}
		return nil
	})
}

func (e *ttyExecer) failVMProvisionISOAttachment(ctx context.Context, plan vmNetworkPlan, cause error) error {
	cleanupErr := plan.ExecuteCleanup(vmProvisionNetworkRunner)
	cleanupErr = errors.Join(cleanupErr, verifyVMISONetworkAbsentForProvision(ctx, plan))
	if cleanupErr != nil {
		return errors.Join(cause, cleanupErr, e.retainFailedVMProvisionISOTombstone(errors.Join(cause, cleanupErr)))
	}
	return errors.Join(cause, e.s.markISOState(e.sn, string(iso.StateQuarantined), cause))
}

func (e *ttyExecer) retainFailedVMProvisionISOTombstone(cause error) error {
	if err := e.s.recordISORemovalIntent(e.sn, false); err != nil {
		return err
	}
	return e.s.markISOState(e.sn, string(iso.StateTombstoned), cause)
}

func writeVMProvisionConfigArtifacts(plan vmProvisionPlan) error {
	if err := writeVMFile(plan.FirecrackerConfigPath, plan.FirecrackerConfig, 0o644); err != nil {
		return fmt.Errorf("write Firecracker config: %w", err)
	}
	if err := markVMJailerReady(plan.ServiceRoot.Root); err != nil {
		return fmt.Errorf("mark VM jailer ready: %w", err)
	}
	return nil
}

//nolint:cyclop // Generation and ISO allocation checks stay adjacent to the atomic DB mutation.
func (e *ttyExecer) commitVMProvision(plan vmProvisionPlan, payload string, snapshotPolicyFlags *cli.ServiceSetFlags) error {
	_, _, err := e.s.cfg.DB.MutateService(e.sn, func(_ *db.Data, s *db.Service) error {
		if plan.Network.hasNetworkMode("iso") {
			if s.ISO == nil || s.ISO.Kind != string(iso.PayloadVM) || s.ISO.RemoveRequested || s.ISO.State != string(iso.StateReserved) {
				return fmt.Errorf("service %q no longer has its reserved VM ISO allocation", e.sn)
			}
			if !vmNetworkMatchesISOAllocation(plan.Network, s.ISO) {
				return fmt.Errorf("service %q ISO allocation changed before provision commit", e.sn)
			}
		} else if s.ISO != nil {
			return fmt.Errorf("service %q has an ISO allocation but the VM plan is not ISO", e.sn)
		}
		applyVMServiceRoot(s, e.s.defaultServiceRootDir(e.sn), plan.ServiceRoot)
		s.ServiceType = db.ServiceTypeVM
		if plan.SvcNetwork != nil {
			s.SvcNetwork = plan.SvcNetwork
		}
		s.VM = &db.VMConfig{
			Runtime: vmRuntimeFirecracker,
			Image: db.VMImageConfig{
				Payload:         payload,
				Version:         plan.Image.Manifest.Version,
				Kernel:          plan.Image.Paths.KernelPath,
				RootFS:          plan.Image.DiskRootFSPath(),
				Distro:          plan.Image.Manifest.Distro,
				DistroVersion:   plan.Image.Manifest.DistroVersion,
				DefaultUser:     plan.Metadata.User,
				GuestSystemInit: plan.Image.Manifest.GuestSystemInit,
				MetadataDriver:  plan.Metadata.MetadataDriver,
			},
			CPUs:        plan.Shape.CPUs,
			MemoryBytes: plan.Shape.MemoryBytes,
			Balloon:     plan.Balloon,
			Disk: db.VMDiskConfig{
				Backend: plan.Shape.DiskBackend,
				Bytes:   plan.Shape.DiskBytes,
				Path:    plan.DiskPath,
			},
			Networks: plan.Network.DBNetworks(),
			SSH:      db.VMSSHConfig{User: plan.Metadata.User},
			Console:  db.VMConsoleConfig{SocketPath: plan.SerialSocket, LogPath: plan.SerialLog},
			Sockets: db.VMSocketConfig{
				APISocketPath:   plan.APISocket,
				VsockSocketPath: plan.VsockSocket,
				VsockGuestCID:   vmAgentGuestCID,
			},
			PIDFile:    plan.PIDFile,
			SetupState: "ready",
		}
		if snapshotPolicyFlags != nil {
			if err := applySnapshotFlagsToService(s, *snapshotPolicyFlags); err != nil {
				return err
			}
		}
		if plan.Network.hasNetworkMode("iso") {
			s.ISO.State = string(iso.StateStopped)
			s.ISO.LastError = ""
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("commit VM service: %w", err)
	}
	return nil
}

func (e *ttyExecer) installVMSystemdUnit(plan vmProvisionPlan, ui *vmProvisionUI) (unitTouched bool, err error) {
	ui.StartStep(vmRunStepInstall)
	doneWriteUnit := e.traceBlock("vm install unit file")
	if err := writeVMFile(plan.SystemdUnitInstallPath, []byte(plan.SystemdUnitContent), 0o644); err != nil {
		doneWriteUnit()
		retErr := fmt.Errorf("install VM systemd unit: %w", err)
		ui.FailStep(retErr.Error())
		return unitTouched, retErr
	}
	unitTouched = true
	doneWriteUnit()
	systemctl := vmProvisionSystemctlFunc
	if systemctl == nil {
		systemctl = runVMSystemctl
	}
	unit := filepath.Base(plan.SystemdUnitInstallPath)
	doneReload := e.traceBlock("vm systemd daemon-reload")
	if err := systemctl("daemon-reload"); err != nil {
		doneReload()
		ui.FailStep(err.Error())
		return unitTouched, err
	}
	doneReload()
	doneEnable := e.traceBlock("vm systemd enable")
	if err := systemctl("enable", unit); err != nil {
		doneEnable()
		ui.FailStep(err.Error())
		return unitTouched, err
	}
	doneEnable()
	ui.DoneStep("")
	return unitTouched, nil
}

func (e *ttyExecer) restartVMSystemdUnit(plan vmProvisionPlan) error {
	systemctl := vmProvisionSystemctlFunc
	if systemctl == nil {
		systemctl = runVMSystemctl
	}
	return systemctl("restart", filepath.Base(plan.SystemdUnitInstallPath))
}

func writeVMFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func runVMSystemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %v failed: %w\n%s", args, err, string(out))
	}
	return nil
}

func formatVMProvisionNetwork(plan vmNetworkPlan) string {
	modes := make([]string, 0, len(plan.Interfaces))
	for _, iface := range plan.Interfaces {
		if iface.Mode != "" {
			modes = append(modes, iface.Mode)
		}
	}
	if len(modes) == 0 {
		return "none"
	}
	return strings.Join(modes, ",")
}

func formatVMProvisionBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	unit := "B"
	for _, next := range []string{"KB", "MB", "GB", "TB"} {
		if value < 1024 {
			break
		}
		value /= 1024
		unit = next
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}

func applyVMServiceRoot(s *db.Service, defaultRoot string, resolved resolvedServiceRoot) {
	s.ServiceRootZFS = resolved.Dataset
	if filepath.Clean(resolved.Root) == filepath.Clean(defaultRoot) && resolved.Dataset == "" {
		s.ServiceRoot = ""
		return
	}
	s.ServiceRoot = resolved.Root
}

func vmDiskPathForRuntime(plan vmDiskPlan) string {
	if plan.Backend == vmDiskBackendZVOL {
		return "/dev/zvol/" + strings.TrimPrefix(plan.Path, "/")
	}
	return plan.Path
}

func (e *ttyExecer) runningVMMemory() (int64, int64, error) {
	return e.s.runningVMMemoryExcluding("")
}

func (e *ttyExecer) reserveVMServiceNetwork(flags cli.RunFlags) (*db.SvcNetwork, error) {
	if !vmNetworkModeRequested(flags.Net, "svc") {
		return nil, nil
	}
	_, service, err := e.s.cfg.DB.MutateService(e.sn, func(d *db.Data, s *db.Service) error {
		if s.SvcNetwork != nil && s.SvcNetwork.IPv4.IsValid() {
			return nil
		}
		svcNet, err := svcNetworkFromData(d.View())
		if err != nil {
			return err
		}
		s.SvcNetwork = svcNet
		return nil
	})
	if err != nil {
		return nil, err
	}
	if service == nil || service.SvcNetwork == nil || !service.SvcNetwork.IPv4.IsValid() {
		return nil, fmt.Errorf("failed to reserve VM service IP")
	}
	return &db.SvcNetwork{IPv4: service.SvcNetwork.IPv4}, nil
}

func (e *ttyExecer) reserveVMISONetwork(flags cli.RunFlags) (*db.ISOAllocation, error) {
	if !vmNetworkModeRequested(flags.Net, "iso") {
		return nil, nil
	}
	modes, err := iso.NormalizeModes(vmNetworkModes(vmRequestedNetworkModes(flags.Net)))
	if err != nil {
		return nil, err
	}
	if err := iso.ValidateNetwork(iso.NetworkRequest{Payload: iso.PayloadVM, Modes: modes}); err != nil {
		return nil, err
	}
	ctx := e.vmProvisionContext()
	if err := e.s.ensureISOPool(ctx); err != nil {
		return nil, err
	}
	var result *db.ISOAllocation
	_, _, err = e.s.cfg.DB.MutateService(e.sn, func(data *db.Data, service *db.Service) error {
		allocation, reserveErr := reserveISOAllocationInData(e.sn, isoReservationRequest{Kind: iso.PayloadVM, Modes: modes}, data, service)
		if reserveErr != nil {
			return reserveErr
		}
		service.ServiceType = db.ServiceTypeVM
		service.VM = &db.VMConfig{
			Runtime: vmRuntimeFirecracker,
			Networks: newVMNetworkPlan(e.sn, []string{"iso"}, vmNetworkInputs{
				ISOHostIP: allocation.HostIP, ISOGuestIP: allocation.PeerIP,
				ISOLink: allocation.Link, ISOTap: allocation.Interface,
			}).DBNetworks(),
			SetupState: "reserved",
		}
		result = cloneVMISOAllocation(allocation)
		return nil
	})
	return result, err
}

func (e *ttyExecer) vmNetworkPlanFromFlags(flags cli.RunFlags, svcNet *db.SvcNetwork, isoAllocations ...*db.ISOAllocation) (vmNetworkPlan, error) {
	input := vmNetworkInputs{
		LANParent: strings.TrimSpace(flags.MacvlanParent),
		LANVLAN:   flags.MacvlanVlan,
		LANMAC:    strings.TrimSpace(flags.MacvlanMac),
	}
	if svcNet != nil && svcNet.IPv4.IsValid() {
		input.ServiceIP = svcNet.IPv4.String()
	}
	if len(isoAllocations) > 0 && isoAllocations[0] != nil {
		allocation := isoAllocations[0]
		input.ISOHostIP = allocation.HostIP
		input.ISOGuestIP = allocation.PeerIP
		input.ISOLink = allocation.Link
		input.ISOTap = allocation.Interface
	}
	modes := vmRequestedNetworkModes(flags.Net)
	if err := validateVMNetworkOptions(modes, flags.MacvlanParent, flags.MacvlanVlan, flags.MacvlanMac); err != nil {
		return vmNetworkPlan{}, err
	}
	if vmModeListContains(modes, "lan") {
		if err := resolveVMLANNetworkInput(&input); err != nil {
			return vmNetworkPlan{}, err
		}
		if input.LANMAC == "" {
			input.LANMAC = randomMAC()
		}
	}
	return newVMNetworkPlan(e.sn, modes, input), nil
}

func validateVMNetworkModes(modes []string) error {
	seen := map[string]bool{}
	for _, raw := range modes {
		for _, part := range strings.Split(raw, ",") {
			mode := strings.TrimSpace(part)
			if mode == "" {
				return fmt.Errorf("VM network mode must not be empty; supported modes: svc, lan")
			}
			if err := validateVMNetworkMode(mode, seen); err != nil {
				return err
			}
		}
	}
	if seen["iso"] && len(seen) != 1 {
		return fmt.Errorf("VMs support only iso as a Yeet-managed isolated mode")
	}
	return nil
}

func validateVMNetworkMode(mode string, seen map[string]bool) error {
	switch mode {
	case "svc", "lan", "iso":
	default:
		return fmt.Errorf("unsupported VM network mode %q; supported modes: svc, lan, iso", mode)
	}
	if seen[mode] {
		return fmt.Errorf("duplicate VM network mode %q; supported modes: svc, lan, iso", mode)
	}
	seen[mode] = true
	return nil
}

func validateVMNetworkOptions(modes []string, macvlanParent string, macvlanVLAN int, macvlanMAC string) error {
	if err := validateVMNetworkModes(modes); err != nil {
		return err
	}
	if macvlanVLAN < 0 || macvlanVLAN > 4094 {
		return fmt.Errorf("--macvlan-vlan must be between 1 and 4094")
	}
	if !vmMacvlanOptionsSet(macvlanParent, macvlanVLAN, macvlanMAC) || vmModeListContains(modes, "lan") {
		return nil
	}
	return fmt.Errorf("--macvlan-* settings require VM LAN networking; use --net=lan or --net=svc,lan")
}

func vmMacvlanOptionsSet(parent string, vlan int, mac string) bool {
	return strings.TrimSpace(parent) != "" || vlan != 0 || strings.TrimSpace(mac) != ""
}

func vmRequestedNetworkModes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{"svc"}
	}
	return []string{raw}
}

func vmNetworkModeRequested(raw, mode string) bool {
	return vmModeListContains(vmRequestedNetworkModes(raw), mode)
}

func vmModeListContains(modes []string, want string) bool {
	for _, mode := range vmNetworkModes(modes) {
		if mode == want {
			return true
		}
	}
	return false
}

func vmLANParentIsBridge(parent string) bool {
	if parent == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join("/sys/class/net", parent, "bridge")); err == nil {
		return true
	}
	return strings.HasPrefix(parent, "br") || strings.HasPrefix(parent, "vmbr")
}

func vmZVOLBaseDataset(root resolvedServiceRoot, version string) string {
	dataset := strings.Trim(root.Dataset, "/")
	pool := vmZVOLPoolName(dataset)
	if pool == "" {
		return "yeet/vm-images/" + version + "/root"
	}
	return pool + "/yeet/vm-images/" + version + "/root"
}

func vmZVOLPoolName(dataset string) string {
	dataset = strings.Trim(dataset, "/")
	if dataset == "" {
		return ""
	}
	if idx := strings.Index(dataset, "/"); idx > 0 {
		return dataset[:idx]
	}
	return dataset
}

func vmZVOLRootDataset(root resolvedServiceRoot, service string) string {
	dataset := strings.Trim(root.Dataset, "/")
	if dataset == "" {
		dataset = "yeet/vms"
	}
	return dataset + "/root"
}
