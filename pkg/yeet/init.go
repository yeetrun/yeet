// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/shayne/yargs"
)

var archMap = map[string]string{
	"x86_64":  "amd64",
	"i386":    "386",
	"i686":    "386",
	"armv7l":  "arm",
	"armv8l":  "arm64",
	"aarch64": "arm64",
	"ppc64le": "ppc64le",
	"s390x":   "s390x",
}

type initFlagsParsed struct {
	FromGithub     bool   `flag:"from-github"`
	Nightly        bool   `flag:"nightly"`
	InstallDocker  bool   `flag:"install-docker"`
	InstallVMTools bool   `flag:"install-vm-tools"`
	TSAuthKey      string `flag:"ts-auth-key"`
	TSClientSecret string `flag:"ts-client-secret"`
	DataDir        string `flag:"data-dir"`
	ServicesRoot   string `flag:"services-root"`
	ZFS            bool   `flag:"zfs"`
	Workspace      string `flag:"workspace"`
	NoWorkspace    bool   `flag:"no-workspace"`
}

type initOptions struct {
	fromGithub         bool
	nightly            bool
	installDocker      bool
	installVMTools     bool
	prepareVMLANBridge bool
	skipVMLANBridge    bool
	noWorkspace        bool
	suppressNextSteps  bool
	workspace          string
	tsAuthKey          string
	tsClientSecret     string
	releaseVersion     string
	storage            initStorageOptions
}

var initCatchFn = initCatch

const defaultCatchTag = "tag:catch"

const hostRequirementsDocsURL = "https://yeetrun.com/docs/getting-started/installation#host-requirements"

var (
	errTailscaleCredentialRequired = errors.New("tailscale credential required")
	errTailscaleOAuthRejected      = errors.New("tailscale oauth setup rejected")
)

func HandleInit(_ context.Context, args []string) error {
	pos, opts, err := parseInitArgs(args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return updateCatch(opts)
	}
	return initCatchFn(pos[0], opts)
}

func parseInitArgs(args []string) ([]string, initOptions, error) {
	if len(args) > 0 && args[0] == "init" {
		args = args[1:]
	}
	result, err := yargs.ParseFlags[initFlagsParsed](args)
	if err != nil {
		return nil, initOptions{}, err
	}
	pos := append([]string{}, result.Args...)
	if len(result.RemainingArgs) > 0 {
		pos = append(pos, result.RemainingArgs...)
	}
	opts, err := initOptionsFromParsedFlags(result.Flags)
	if err != nil {
		return nil, initOptions{}, err
	}
	if err := validateInitArgs(pos, opts); err != nil {
		return nil, initOptions{}, err
	}
	return pos, opts, nil
}

func initOptionsFromParsedFlags(flags initFlagsParsed) (initOptions, error) {
	storage, err := initStorageOptionsFromFlags(flags)
	if err != nil {
		return initOptions{}, err
	}
	return initOptions{
		fromGithub:     flags.FromGithub,
		nightly:        flags.Nightly,
		installDocker:  flags.InstallDocker,
		installVMTools: flags.InstallVMTools,
		noWorkspace:    flags.NoWorkspace,
		workspace:      flags.Workspace,
		tsAuthKey:      flags.TSAuthKey,
		tsClientSecret: flags.TSClientSecret,
		storage:        storage,
	}, nil
}

func validateInitArgs(pos []string, opts initOptions) error {
	if opts.tsAuthKey != "" && opts.tsClientSecret != "" {
		return fmt.Errorf("--ts-auth-key and --ts-client-secret cannot be used together")
	}
	if opts.tsClientSecret != "" && !strings.HasPrefix(opts.tsClientSecret, "tskey-client-") {
		return fmt.Errorf("invalid --ts-client-secret (expected tskey-client-...)")
	}
	if strings.TrimSpace(opts.workspace) != "" && opts.noWorkspace {
		return fmt.Errorf("--workspace and --no-workspace cannot be used together")
	}
	if len(pos) > 1 {
		return fmt.Errorf("init takes at most one argument")
	}
	return nil
}

// remoteHostOSAndArch returns the system and architecture of a given remote
// host/IP. It uses SSH to run `uname -s` and `uname -m` on the remote host.
// Note that this expects the remote host to be accessible via root@remote.
func remoteHostOSAndArch(userAtRemote string) (system, goarch string, _ error) {
	cmd := exec.Command("ssh", userAtRemote, "uname -s && uname -m")
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("SSH command failed: %w", err)
	}
	// Split the output into system name and architecture
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 {
		return "", "", fmt.Errorf("unexpected output from remote: %v", lines)
	}

	system = lines[0]
	arch := lines[1]

	goarch, ok := archMap[arch]
	if !ok {
		log.Fatalf("Unsupported architecture: %s", arch)
	}
	return
}

// remoteCatchOSAndArch fetches the GOOS and GOARCH of the remote host by calling
// the catch RPC info endpoint.
func remoteCatchOSAndArch() (goos, goarch string, _ error) {
	info, err := remoteCatchInfo()
	if err != nil {
		return "", "", err
	}
	return info.GOOS, info.GOARCH, nil
}

func remoteCatchInfo() (serverInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var si serverInfo
	if err := newRPCClient(Host()).Call(ctx, "catch.Info", nil, &si); err != nil {
		return serverInfo{}, fmt.Errorf("failed to get version of catch binary: %w", err)
	}
	return si, nil
}

func updateCatch(opts initOptions) error {
	userAtRemote := Host()
	if info, err := remoteCatchInfo(); err == nil {
		host := userAtRemote
		if info.InstallHost != "" {
			host = info.InstallHost
		}
		if strings.Contains(host, "@") {
			userAtRemote = host
		} else if info.InstallUser != "" {
			userAtRemote = fmt.Sprintf("%s@%s", info.InstallUser, host)
		} else {
			userAtRemote = host
		}
	}
	return initCatchFn(userAtRemote, opts)
}

func buildCatch(goos, goarch, gitRoot string) (string, int64, error) {
	_, goarch, err := normalizeBuildTarget(goos, goarch, gitRoot)
	if err != nil {
		return "", 0, err
	}

	// Build the catch binary
	cmd := buildCatchCmd(goarch, gitRoot)
	if out, err := cmd.CombinedOutput(); err != nil {
		if len(out) > 0 {
			fmt.Fprintln(os.Stderr, string(out))
		}
		return "", 0, fmt.Errorf("failed to build catch binary")
	}
	bin := filepath.Join(gitRoot, "catch")
	info, err := os.Stat(bin)
	if err != nil {
		return "", 0, fmt.Errorf("failed to stat catch binary: %w", err)
	}
	return bin, info.Size(), nil
}

func normalizeBuildTarget(goos, goarch, gitRoot string) (string, string, error) {
	goos = strings.ToLower(goos)
	goarch = strings.ToLower(goarch)
	if goos != "linux" {
		return "", "", fmt.Errorf("remote system is not Linux: %s", goos)
	}
	if gitRoot == "" {
		return "", "", fmt.Errorf("missing git root for catch build")
	}
	return goos, goarch, nil
}

func buildCatchCmd(goarch, gitRoot string) *exec.Cmd {
	args := []string{"build", "-o", "catch"}
	if version := localBuildVersion(gitRoot); version != "" {
		args = append(args, "-ldflags", "-X github.com/yeetrun/yeet/pkg/buildinfo.BuildVersion="+version)
	}
	args = append(args, "./cmd/catch")
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOARCH="+goarch, "GOOS=linux", "CGO_ENABLED=0")
	cmd.Dir = gitRoot
	return cmd
}

func localBuildVersion(gitRoot string) string {
	out, err := gitWorkTreeCommand(gitRoot, "rev-parse", "--short=9", "HEAD").Output()
	if err != nil {
		return ""
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return ""
	}
	if localGitHasTrackedChanges(gitRoot) {
		version += "+dirty"
	}
	return version
}

func localGitHasTrackedChanges(gitRoot string) bool {
	cmd := gitWorkTreeCommand(gitRoot, "diff", "--quiet")
	unstagedDirty := cmd.Run() != nil
	cmd = gitWorkTreeCommand(gitRoot, "diff", "--cached", "--quiet")
	stagedDirty := cmd.Run() != nil
	return unstagedDirty || stagedDirty
}

func gitWorkTreeCommand(gitRoot string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", gitRoot}, args...)...)
	cmd.Env = gitWorkTreeEnv(os.Environ())
	return cmd
}

func gitWorkTreeEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_PREFIX", "GIT_COMMON_DIR":
			continue
		default:
			out = append(out, item)
		}
	}
	return out
}

func initCatch(userAtRemote string, opts initOptions) (err error) {
	useSudo := shouldUseSudoForInit(userAtRemote)
	isTTY := isTerminalFn(int(os.Stdout.Fd()))
	enabled, quiet := initProgressSettings(execProgressMode(), isTTY)
	ui := newInitUI(os.Stdout, enabled, quiet, Host(), userAtRemote, catchServiceName)
	ui.Start()
	defer ui.Stop()

	ui.StartStep("Check local")
	source, err := resolveInitCatchSourceFn(opts)
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	ui.DoneStep(source.localDetail())

	if err := verifyInitSSHFn(ui, userAtRemote); err != nil {
		return err
	}

	systemName, goarch, err := detectInitHostFn(ui, userAtRemote)
	if err != nil {
		return err
	}

	installDocker, err := prepareInitDockerInstallFn(ui, userAtRemote, opts)
	if err != nil {
		return err
	}

	installVMTools, err := prepareInitVMToolsInstallFn(ui, userAtRemote, goarch, opts)
	if err != nil {
		return err
	}

	storage, err := prepareInitStorageOptionsFn(ui, userAtRemote, useSudo, opts)
	if err != nil {
		return err
	}
	storage = withInitCatchRemoteBinary(storage, useSudo)
	opts.storage = storage

	if err := prepareInitCatchBinary(ui, userAtRemote, systemName, goarch, source, opts); err != nil {
		return err
	}

	if err := chmodInitCatchFn(ui, userAtRemote, opts.storage.catchRemoteBinary()); err != nil {
		return err
	}
	if err := finalizeInitCatch(ui, userAtRemote, goarch, useSudo, installDocker, installVMTools, opts); err != nil {
		return err
	}
	return nil
}

func finalizeInitCatch(ui *initUI, userAtRemote string, goarch string, useSudo bool, installDocker bool, installVMTools bool, opts initOptions) error {
	prepareInitVMLANBridge(ui, userAtRemote, goarch, installVMTools, &opts)
	if err := installInitCatchWithTailscaleRetry(ui, userAtRemote, useSudo, installDocker, installVMTools, opts); err != nil {
		return err
	}
	cleanupInitCatchBinaryFn(ui, userAtRemote, opts.storage.catchRemoteBinary())
	if err := finishInitWorkspaceSetup(ui, opts); err != nil {
		return err
	}
	return nil
}

func prepareInitCatchBinary(ui *initUI, userAtRemote string, systemName string, goarch string, source initCatchSource, opts initOptions) error {
	if source.useGithub {
		return downloadInitCatchFn(ui, userAtRemote, systemName, goarch, opts.nightly, opts.releaseVersion, opts.storage.catchRemoteBinary())
	}
	return buildAndUploadInitCatchFn(ui, userAtRemote, systemName, goarch, source, opts.storage.catchRemoteBinary())
}

type initCatchSource struct {
	useGithub bool
	gitRoot   string
	goVersion string
	reason    string
}

var (
	resolveInitCatchSourceFn    = resolveInitCatchSource
	verifyInitSSHFn             = verifyInitSSH
	detectInitHostFn            = detectInitHost
	downloadInitCatchFn         = downloadInitCatch
	buildAndUploadInitCatchFn   = buildAndUploadInitCatch
	chmodInitCatchFn            = chmodInitCatch
	installInitCatchFn          = installInitCatch
	cleanupInitCatchBinaryFn    = cleanupInitCatchBinary
	prepareInitDockerInstallFn  = prepareInitDockerInstall
	prepareInitVMToolsInstallFn = prepareInitVMToolsInstall
	remoteDockerInstalledFn     = remoteDockerInstalled
	remoteVMHostStatusFn        = remoteVMHostStatus
	remoteVMLANBridgePlanFn     = remoteVMLANBridgePlan
)

func (s initCatchSource) localDetail() string {
	if s.useGithub {
		return s.reason
	}
	return s.goVersion
}

func shouldUseSudoForInit(userAtRemote string) bool {
	if user, _, ok := strings.Cut(userAtRemote, "@"); ok && user == "root" {
		return false
	}
	fmt.Fprint(os.Stderr, color.RedString("Warning: root is required to install catch on the remote host.\nsudo will be used which may require a password.\n\n"))
	return true
}

func resolveInitCatchSource(opts initOptions) (initCatchSource, error) {
	if opts.fromGithub {
		return initCatchSource{useGithub: true, reason: "using GitHub release"}, nil
	}
	root, ok, err := localCatchRepoRoot()
	if err != nil {
		return initCatchSource{}, err
	}
	if !ok {
		return initCatchSource{useGithub: true, reason: "no local checkout; using GitHub release"}, nil
	}
	goVersion, err := localGoVersion(root)
	if err != nil {
		return initCatchSource{}, err
	}
	return initCatchSource{gitRoot: root, goVersion: goVersion}, nil
}

func verifyInitSSH(ui *initUI, userAtRemote string) error {
	if !isTerminalFn(int(os.Stdin.Fd())) || !isTerminalFn(int(os.Stderr.Fd())) {
		return nil
	}
	ui.StartStep("Verify SSH")
	ui.Suspend()
	err := preflightSSHHostKey(userAtRemote)
	ui.Resume()
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	ui.DoneStep("")
	return nil
}

func detectInitHost(ui *initUI, userAtRemote string) (string, string, error) {
	ui.StartStep("Detect host")
	systemName, goarch, err := remoteHostOSAndArch(userAtRemote)
	if err != nil {
		ui.FailStep(err.Error())
		return "", "", err
	}
	ui.DoneStep(fmt.Sprintf("%s/%s", strings.ToLower(systemName), goarch))
	return systemName, goarch, nil
}

func prepareInitDockerInstall(ui *initUI, userAtRemote string, opts initOptions) (bool, error) {
	ui.StartStep("Check Docker")
	if opts.installDocker {
		ui.DoneStep("will install")
		return true, nil
	}
	installed, err := remoteDockerInstalledFn(userAtRemote)
	if err != nil {
		ui.FailStep(err.Error())
		return false, err
	}
	if installed {
		ui.DoneStep("present")
		return false, nil
	}
	if !isTerminalFn(int(os.Stdin.Fd())) || !isTerminalFn(int(os.Stdout.Fd())) {
		err := fmt.Errorf("docker is required on the remote host; rerun yeet init with --install-docker or install Docker manually")
		ui.FailStep("docker missing")
		return false, err
	}
	ui.Suspend()
	ok, err := activePrompter.Confirm("Docker is required on the remote host. Install Docker now?", false)
	ui.Resume()
	if err != nil {
		ui.FailStep(err.Error())
		return false, err
	}
	if !ok {
		err := fmt.Errorf("docker is required on the remote host; rerun yeet init with --install-docker or install Docker manually")
		ui.FailStep("docker missing")
		return false, err
	}
	ui.DoneStep("will install")
	return true, nil
}

func remoteDockerInstalled(userAtRemote string) (bool, error) {
	const probe = "if command -v docker >/dev/null 2>&1; then printf yes; else printf no; fi"
	cmd := exec.Command("ssh", userAtRemote, "bash -lc "+shellQuote(probe))
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to check docker on remote host: %w", err)
	}
	switch strings.TrimSpace(string(output)) {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected docker check output from remote host: %q", strings.TrimSpace(string(output)))
	}
}

type vmHostCommandRequirement struct {
	Command string
	Package string
}

type initVMHostStatus struct {
	AptGet          bool
	KVM             bool
	TUN             bool
	MissingCommands []vmHostCommandRequirement
	UnsupportedArch string
}

type initVMLANBridgePlan struct {
	Ready        bool
	NeedsPrepare bool
	Bridge       string
	Parent       string
	Reason       string
}

var requiredInitVMHostCommands = []vmHostCommandRequirement{
	{Command: "qemu-img", Package: "qemu-utils"},
	{Command: "zstd", Package: "zstd"},
	{Command: "e2fsck", Package: "e2fsprogs"},
	{Command: "resize2fs", Package: "e2fsprogs"},
	{Command: "mount", Package: "util-linux"},
	{Command: "umount", Package: "util-linux"},
	{Command: "ip", Package: "iproute2"},
}

func prepareInitVMToolsInstall(ui *initUI, userAtRemote, goarch string, opts initOptions) (bool, error) {
	ui.StartStep("Check VM tools")
	if opts.installVMTools {
		ui.DoneStep("will install")
		return true, nil
	}
	status, err := remoteVMHostStatusFn(userAtRemote, goarch)
	if err != nil {
		ui.DoneStep("skipped")
		ui.Warn(formatInitVMToolsPreflightWarning(err))
		return false, nil
	}
	if initVMHostCapabilityUnavailable(status) {
		ui.DoneStep("not available")
		return false, nil
	}
	if len(status.MissingCommands) == 0 {
		ui.DoneStep("present")
		return false, nil
	}
	packages := missingInitVMHostPackages(status.MissingCommands)
	if !status.AptGet {
		ui.DoneStep("manual install")
		ui.Warn(formatInitVMToolsMissingWarning(status.MissingCommands, packages))
		return false, nil
	}
	if !isTerminalFn(int(os.Stdin.Fd())) || !isTerminalFn(int(os.Stdout.Fd())) {
		ui.DoneStep("manual install")
		ui.Warn(formatInitVMToolsNonInteractiveWarning(status.MissingCommands, packages))
		return false, nil
	}
	ui.Suspend()
	ok, err := activePrompter.Confirm("VM payloads can run on this host, but VM host packages are missing. Install them now?", false)
	ui.Resume()
	if err != nil {
		ui.DoneStep("manual install")
		ui.Warn(fmt.Sprintf("Warning: could not confirm VM package install (%v). To enable VM payloads, install: %s. See %s", err, strings.Join(packages, ", "), hostRequirementsDocsURL))
		return false, nil
	}
	if !ok {
		ui.DoneStep("manual install")
		ui.Warn(formatInitVMToolsMissingWarning(status.MissingCommands, packages))
		return false, nil
	}
	ui.DoneStep("will install")
	return true, nil
}

func prepareInitVMLANBridge(ui *initUI, userAtRemote string, goarch string, installVMTools bool, opts *initOptions) {
	if opts == nil || opts.prepareVMLANBridge || opts.skipVMLANBridge {
		return
	}
	plan, ok := initVMLANBridgePlanForInit(ui, userAtRemote, goarch, installVMTools, opts)
	if !ok {
		return
	}
	applyInitVMLANBridgePlan(ui, userAtRemote, plan, opts)
}

func initVMLANBridgePlanForInit(ui *initUI, userAtRemote string, goarch string, installVMTools bool, opts *initOptions) (initVMLANBridgePlan, bool) {
	if !initVMLANBridgeSupportPlausible(userAtRemote, goarch, installVMTools) {
		return initVMLANBridgePlan{}, false
	}
	plan, err := remoteVMLANBridgePlanFn(userAtRemote, opts.storage)
	if err != nil {
		opts.skipVMLANBridge = true
		ui.Warn(fmt.Sprintf("Warning: VM LAN bridge planning is unavailable during init: %v", err))
		return initVMLANBridgePlan{}, false
	}
	if plan.Ready || !plan.NeedsPrepare {
		return initVMLANBridgePlan{}, false
	}
	return plan, true
}

func applyInitVMLANBridgePlan(ui *initUI, userAtRemote string, plan initVMLANBridgePlan, opts *initOptions) {
	bridge := strings.TrimSpace(plan.Bridge)
	if bridge == "" {
		bridge = "br0"
	}
	if !canPromptInitVMLANBridge() {
		opts.skipVMLANBridge = true
		ui.Warn(fmt.Sprintf("Warning: VM LAN bridge %s needs preparation for VM LAN networking. `yeet run ... --net=lan` will need an interactive run, or rerun `yeet init %s` in a TTY.", bridge, userAtRemote))
		return
	}
	ui.Suspend()
	ok, err := activePrompter.Confirm(fmt.Sprintf("Prepare %s for VM LAN networking during init?", bridge), false)
	ui.Resume()
	if err != nil {
		opts.skipVMLANBridge = true
		ui.Warn(fmt.Sprintf("Warning: could not confirm VM LAN bridge preparation during init: %v. `yeet run ... --net=lan` will need an interactive run, or rerun `yeet init %s` in a TTY.", err, userAtRemote))
		return
	}
	if !ok {
		opts.skipVMLANBridge = true
		return
	}
	opts.prepareVMLANBridge = true
}

func initVMLANBridgeSupportPlausible(userAtRemote, goarch string, installVMTools bool) bool {
	status, err := remoteVMHostStatusFn(userAtRemote, goarch)
	if err != nil {
		return false
	}
	if status.UnsupportedArch != "" || !status.KVM || !status.TUN {
		return false
	}
	return installVMTools || len(status.MissingCommands) == 0
}

func canPromptInitVMLANBridge() bool {
	return isTerminalFn(int(os.Stdin.Fd())) && isTerminalFn(int(os.Stdout.Fd()))
}

func initVMHostCapabilityUnavailable(status initVMHostStatus) bool {
	return status.UnsupportedArch != "" || !status.KVM || !status.TUN
}

func formatInitVMToolsMissingWarning(missing []vmHostCommandRequirement, packages []string) string {
	return fmt.Sprintf(
		"Warning: VM tools are incomplete: missing %s. Install packages: %s. See %s",
		strings.Join(initVMHostCommandNames(missing), ", "),
		strings.Join(packages, ", "),
		hostRequirementsDocsURL,
	)
}

func formatInitVMToolsNonInteractiveWarning(missing []vmHostCommandRequirement, packages []string) string {
	return fmt.Sprintf(
		"Warning: VM tools are incomplete: missing %s. Rerun yeet init with --install-vm-tools for unattended setup, or install packages: %s. See %s",
		strings.Join(initVMHostCommandNames(missing), ", "),
		strings.Join(packages, ", "),
		hostRequirementsDocsURL,
	)
}

func formatInitVMToolsPreflightWarning(err error) string {
	return fmt.Sprintf(
		"Warning: could not check VM host packages on remote host (%v). Continuing without VM host package installation; rerun yeet init with --install-vm-tools for unattended setup or review host requirements. See %s",
		err,
		hostRequirementsDocsURL,
	)
}

func missingInitVMHostPackages(missing []vmHostCommandRequirement) []string {
	packages := make([]string, 0, len(missing))
	for _, req := range missing {
		if req.Package != "" {
			packages = append(packages, req.Package)
		}
	}
	return sortedUniqueStrings(packages)
}

func initVMHostCommandNames(reqs []vmHostCommandRequirement) []string {
	names := make([]string, 0, len(reqs))
	for _, req := range reqs {
		if req.Command != "" {
			names = append(names, req.Command)
		}
	}
	return names
}

func remoteVMLANBridgePlan(userAtRemote string, storage initStorageOptions) (initVMLANBridgePlan, error) {
	args := append([]string{userAtRemote}, catchLocalCommand(storage, "vm-lan-bridge-plan")...)
	cmd := exec.Command("ssh", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return initVMLANBridgePlan{}, fmt.Errorf("remote VM LAN bridge plan failed: %w", err)
		}
		return initVMLANBridgePlan{}, fmt.Errorf("remote VM LAN bridge plan failed: %w: %s", err, detail)
	}
	plan, err := parseInitVMLANBridgePlan(string(output))
	if err != nil {
		return initVMLANBridgePlan{}, fmt.Errorf("unexpected VM LAN bridge plan output from remote host: %w", err)
	}
	return plan, nil
}

func parseInitVMLANBridgePlan(output string) (initVMLANBridgePlan, error) {
	fields, err := initVMLANBridgePlanFields(output)
	if err != nil {
		return initVMLANBridgePlan{}, err
	}
	var plan initVMLANBridgePlan
	var sawReady, sawNeedsPrepare bool
	for i := 0; i < len(fields); i++ {
		stop, err := applyInitVMLANBridgePlanField(&plan, fields[i], fields[i:], &sawReady, &sawNeedsPrepare)
		if err != nil {
			return initVMLANBridgePlan{}, err
		}
		if stop {
			break
		}
	}
	if !sawReady || !sawNeedsPrepare {
		return initVMLANBridgePlan{}, fmt.Errorf("missing ready or needs_prepare field")
	}
	return plan, nil
}

func initVMLANBridgePlanFields(output string) ([]string, error) {
	const prefix = "VM LAN bridge plan:"
	line := strings.TrimSpace(output)
	idx := strings.LastIndex(line, prefix)
	if idx < 0 {
		return nil, fmt.Errorf("missing %q prefix", prefix)
	}
	return strings.Fields(strings.TrimSpace(line[idx+len(prefix):])), nil
}

func applyInitVMLANBridgePlanField(plan *initVMLANBridgePlan, field string, remaining []string, sawReady *bool, sawNeedsPrepare *bool) (bool, error) {
	if strings.HasPrefix(field, "reason=") {
		plan.Reason = strings.TrimPrefix(strings.Join(remaining, " "), "reason=")
		return true, nil
	}
	key, value, ok := strings.Cut(field, "=")
	if !ok {
		return false, fmt.Errorf("malformed field %q", field)
	}
	return applyInitVMLANBridgePlanKey(plan, key, value, sawReady, sawNeedsPrepare)
}

func applyInitVMLANBridgePlanKey(plan *initVMLANBridgePlan, key string, value string, sawReady *bool, sawNeedsPrepare *bool) (bool, error) {
	switch key {
	case "bridge":
		plan.Bridge = value
	case "parent":
		plan.Parent = value
	case "ready":
		ready, err := parseInitVMLANBridgePlanBool(value)
		if err != nil {
			return false, fmt.Errorf("ready: %w", err)
		}
		plan.Ready = ready
		*sawReady = true
	case "needs_prepare":
		needsPrepare, err := parseInitVMLANBridgePlanBool(value)
		if err != nil {
			return false, fmt.Errorf("needs_prepare: %w", err)
		}
		plan.NeedsPrepare = needsPrepare
		*sawNeedsPrepare = true
	default:
		return false, fmt.Errorf("unknown field %q", key)
	}
	return false, nil
}

func parseInitVMLANBridgePlanBool(value string) (bool, error) {
	switch strings.TrimSpace(value) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected value %q", value)
	}
}

func remoteVMHostStatus(userAtRemote, goarch string) (initVMHostStatus, error) {
	commands := shellJoin(initVMHostCommandNames(requiredInitVMHostCommands))
	probe := fmt.Sprintf(`if command -v apt-get >/dev/null 2>&1; then printf 'apt-get=yes\n'; else printf 'apt-get=no\n'; fi
if [ -e /dev/kvm ]; then printf 'dev-kvm=yes\n'; else printf 'dev-kvm=no\n'; fi
if [ -e /dev/net/tun ]; then printf 'dev-net-tun=yes\n'; else printf 'dev-net-tun=no\n'; fi
for cmd in %s; do
	if command -v "$cmd" >/dev/null 2>&1; then printf 'cmd:%%s=yes\n' "$cmd"; else printf 'cmd:%%s=no\n' "$cmd"; fi
done`, commands)
	cmd := exec.Command("ssh", userAtRemote, "bash -lc "+shellQuote(probe))
	output, err := cmd.Output()
	if err != nil {
		return initVMHostStatus{}, fmt.Errorf("failed to check VM host packages on remote host: %w", err)
	}
	status, err := parseInitVMHostStatus(string(output), goarch)
	if err != nil {
		return initVMHostStatus{}, fmt.Errorf("unexpected VM host check output from remote host: %w", err)
	}
	return status, nil
}

func parseInitVMHostStatus(output, goarch string) (initVMHostStatus, error) {
	status := initVMHostStatus{UnsupportedArch: initVMUnsupportedArch(goarch)}
	commandPresent := make(map[string]bool, len(requiredInitVMHostCommands))
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		key, present, err := parseInitVMHostProbeLine(line)
		if err != nil {
			return initVMHostStatus{}, err
		}
		if err := applyInitVMHostProbeValue(&status, commandPresent, key, present); err != nil {
			return initVMHostStatus{}, err
		}
	}
	for _, req := range requiredInitVMHostCommands {
		if !commandPresent[req.Command] {
			status.MissingCommands = append(status.MissingCommands, req)
		}
	}
	return status, nil
}

func initVMUnsupportedArch(goarch string) string {
	normalizedArch := strings.ToLower(strings.TrimSpace(goarch))
	switch normalizedArch {
	case "", "amd64", "x86_64":
		return ""
	default:
		return normalizedArch
	}
}

func parseInitVMHostProbeLine(line string) (string, bool, error) {
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", false, fmt.Errorf("malformed line %q", line)
	}
	present, err := parseInitVMHostProbeBool(value)
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", key, err)
	}
	return key, present, nil
}

func applyInitVMHostProbeValue(status *initVMHostStatus, commandPresent map[string]bool, key string, present bool) error {
	switch {
	case key == "apt-get":
		status.AptGet = present
	case key == "dev-kvm":
		status.KVM = present
	case key == "dev-net-tun":
		status.TUN = present
	case strings.HasPrefix(key, "cmd:"):
		commandPresent[strings.TrimPrefix(key, "cmd:")] = present
	default:
		return fmt.Errorf("unknown key %q", key)
	}
	return nil
}

func parseInitVMHostProbeBool(value string) (bool, error) {
	switch strings.TrimSpace(value) {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected value %q", value)
	}
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	values = append([]string(nil), values...)
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if value == "" {
			continue
		}
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func downloadInitCatch(ui *initUI, userAtRemote, systemName, goarch string, nightly bool, version string, remoteBinary string) error {
	ui.StartStep("Download catch")
	assetName, assetURL, shaURL, tag, err := resolveCatchReleaseAsset(systemName, goarch, nightly, version)
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	downloadDetail, err := downloadCatchRelease(ui, userAtRemote, assetName, assetURL, shaURL, remoteBinary)
	if err != nil {
		ui.FailStep("download failed")
		return err
	}
	ui.DoneStep(fmt.Sprintf("%s (%s)", downloadDetail, tag))
	return nil
}

func buildAndUploadInitCatch(ui *initUI, userAtRemote, systemName, goarch string, source initCatchSource, remoteBinary string) error {
	ui.StartStep("Build catch")
	bin, binSize, err := buildCatch(systemName, goarch, source.gitRoot)
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	defer removeFileBestEffort(bin)

	buildTarget := fmt.Sprintf("%s/%s", strings.ToLower(systemName), goarch)
	ui.DoneStep(formatBuildDetail(source.goVersion, buildTarget, binSize))

	ui.StartStep("Upload catch")
	uploadDetail, err := uploadCatchBinary(ui, bin, binSize, userAtRemote, remoteBinary)
	if err != nil {
		ui.FailStep("upload failed")
		return fmt.Errorf("failed to copy catch binary to remote host: %w", err)
	}
	ui.DoneStep(uploadDetail)
	return nil
}

func formatBuildDetail(goVersion, buildTarget string, binSize int64) string {
	buildDetail := buildTarget
	if strings.TrimSpace(goVersion) != "" {
		buildDetail = fmt.Sprintf("%s -> %s", goVersion, buildTarget)
	}
	if binSize > 0 {
		buildDetail = fmt.Sprintf("%s, %s", buildDetail, humanReadableBytes(float64(binSize)))
	}
	return buildDetail
}

func chmodInitCatch(ui *initUI, userAtRemote string, remoteBinary string) error {
	ui.StartStep("Install catch")
	cmd := exec.Command("ssh", userAtRemote, "chmod", "+x", normalizeInitCatchRemoteBinary(remoteBinary))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		ui.FailStep("chmod failed")
		return fmt.Errorf("failed to make catch binary executable on remote host")
	}
	return nil
}

func cleanupInitCatchBinary(ui *initUI, userAtRemote string, remoteBinary string) {
	binary := normalizeInitCatchRemoteBinary(remoteBinary)
	if binary == "" {
		return
	}
	cmd := exec.Command("ssh", userAtRemote, "rm", "-f", binary)
	if err := cmd.Run(); err != nil {
		ui.Warn(fmt.Sprintf("Warning: could not remove temporary catch installer %s: %v", binary, err))
	}
}

func installInitCatchWithTailscaleRetry(ui *initUI, userAtRemote string, useSudo bool, installDocker bool, installVMTools bool, opts initOptions) error {
	tsClientSecret := strings.TrimSpace(opts.tsClientSecret)
	for attempt := 0; attempt < 3; attempt++ {
		err := installInitCatchFn(ui, userAtRemote, useSudo, installDocker, installVMTools, opts.tsAuthKey, tsClientSecret, []string{defaultCatchTag}, opts.prepareVMLANBridge, opts.skipVMLANBridge, opts.storage)
		if err == nil {
			return nil
		}
		if opts.tsAuthKey != "" || !isInitTailscaleCredentialError(err) {
			return err
		}
		next, promptErr := retryInitTailscaleClientSecret(ui, attempt, err)
		if promptErr != nil {
			return promptErr
		}
		tsClientSecret = next
	}
	return fmt.Errorf("tailscale OAuth setup failed after 3 attempts")
}

func retryInitTailscaleClientSecret(ui *initUI, attempt int, installErr error) (string, error) {
	if !canPromptInitTailscale() {
		return "", fmt.Errorf("%w; run yeet init in a TTY, pass --ts-client-secret=tskey-client-..., or pass --ts-auth-key=<key>; see https://yeetrun.com/docs/concepts/tailscale", installErr)
	}
	ui.Suspend()
	defer ui.Resume()
	if attempt > 0 {
		if _, err := fmt.Fprintln(os.Stdout, "Tailscale rejected that OAuth client secret for tag:catch. Fix the OAuth tags or tagOwners policy, then try another secret."); err != nil {
			return "", err
		}
	}
	for _, line := range []string{
		"yeet installs catch, a small daemon that runs on the host and manages services.",
		"catch uses Tailscale and must join your tailnet as a tagged device, usually tag:catch.",
		"Paste a Tailscale OAuth client secret with the auth_keys scope that can mint tag:catch.",
		"Recommended: create an owner tag such as tag:yeet, let it own tag:catch, and select tag:yeet on the OAuth client.",
		"Docs: https://yeetrun.com/docs/concepts/tailscale",
		"",
	} {
		if _, err := fmt.Fprintln(os.Stdout, line); err != nil {
			return "", err
		}
	}
	next, err := activePrompter.Secret("Tailscale OAuth client secret")
	if err != nil {
		return "", err
	}
	return validateInitTailscaleClientSecret(next)
}

func validateInitTailscaleClientSecret(secret string) (string, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", fmt.Errorf("tailscale OAuth client secret is required")
	}
	if !strings.HasPrefix(secret, "tskey-client-") {
		return "", fmt.Errorf("invalid tailscale OAuth client secret (expected tskey-client-...)")
	}
	return secret, nil
}

func canPromptInitTailscale() bool {
	return isTerminalFn(int(os.Stdin.Fd())) && isTerminalFn(int(os.Stdout.Fd()))
}

func isInitTailscaleCredentialError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errTailscaleCredentialRequired) || errors.Is(err, errTailscaleOAuthRejected) {
		return true
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "tailscale oauth setup failed") ||
		strings.Contains(msg, "requires a Tailscale OAuth client secret or auth key")
}

func installInitCatch(ui *initUI, userAtRemote string, useSudo bool, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string, prepareVMLANBridge bool, skipVMLANBridge bool, storage initStorageOptions) error {
	if !useSudo {
		return installInitCatchDetached(ui, userAtRemote, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags, prepareVMLANBridge, skipVMLANBridge, storage)
	}
	return installInitCatchDirect(ui, userAtRemote, useSudo, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags, prepareVMLANBridge, skipVMLANBridge, storage)
}

func installInitCatchDirect(ui *initUI, userAtRemote string, useSudo bool, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string, prepareVMLANBridge bool, skipVMLANBridge bool, storage initStorageOptions) error {
	cmd := exec.Command("ssh", remoteCatchInstallArgs(userAtRemote, useSudo, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags, prepareVMLANBridge, skipVMLANBridge, storage)...)
	cmd.Stdin = os.Stdin
	ui.Suspend()
	filter := newInitInstallFilter(os.Stdout)
	cmd.Stdout = filter
	cmd.Stderr = filter
	if err := cmd.Run(); err != nil {
		ui.FailStep("install failed")
		if summaryErr := filter.ErrorSummary(); summaryErr != nil {
			return summaryErr
		}
		return fmt.Errorf("failed to run catch binary on remote host")
	}
	ui.DoneStep(filter.SummaryDetail())
	if warn := filter.WarningSummary(); warn != "" {
		ui.Warn(warn)
	}
	if info := filter.InfoSummary(); info != "" {
		ui.Info(info)
	}
	return nil
}

type initInstallSession struct {
	LogPath    string
	StatusPath string
}

var (
	initInstallPollInterval      = 500 * time.Millisecond
	initInstallTimeout           = 5 * time.Minute
	initInstallSSHTimeout        = 10 * time.Second
	readRemoteInitInstallFileFn  = readRemoteInitInstallFile
	streamRemoteInitInstallLogFn = streamRemoteInitInstallLog
)

func installInitCatchDetached(ui *initUI, userAtRemote string, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string, prepareVMLANBridge bool, skipVMLANBridge bool, storage initStorageOptions) error {
	session := newInitInstallSession()
	ui.Suspend()
	filter := newInitInstallFilter(os.Stdout)
	if err := launchDetachedInitCatchInstall(userAtRemote, session, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags, prepareVMLANBridge, skipVMLANBridge, storage); err != nil {
		ui.FailStep("install failed")
		return fmt.Errorf("failed to run catch binary on remote host")
	}
	status, err := waitDetachedInitCatchInstall(userAtRemote, session, filter)
	cleanupDetachedInitCatchInstall(userAtRemote, session)
	if err != nil {
		ui.FailStep("install failed")
		return err
	}
	if strings.TrimSpace(status) != "0" {
		ui.FailStep("install failed")
		if summaryErr := filter.ErrorSummary(); summaryErr != nil {
			return summaryErr
		}
		return fmt.Errorf("failed to run catch binary on remote host")
	}
	ui.DoneStep(filter.SummaryDetail())
	if warn := filter.WarningSummary(); warn != "" {
		ui.Warn(warn)
	}
	if info := filter.InfoSummary(); info != "" {
		ui.Info(info)
	}
	return nil
}

func newInitInstallSession() initInstallSession {
	id := fmt.Sprintf("yeet-catch-install-%d-%d", os.Getpid(), time.Now().UnixNano())
	return initInstallSession{
		LogPath:    "/tmp/" + id + ".log",
		StatusPath: "/tmp/" + id + ".status",
	}
}

func launchDetachedInitCatchInstall(userAtRemote string, session initInstallSession, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string, prepareVMLANBridge bool, skipVMLANBridge bool, storage initStorageOptions) error {
	cmd := exec.Command("ssh", userAtRemote, detachedInitCatchInstallScript(userAtRemote, session, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags, prepareVMLANBridge, skipVMLANBridge, storage))
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func detachedInitCatchInstallScript(userAtRemote string, session initInstallSession, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string, prepareVMLANBridge bool, skipVMLANBridge bool, storage initStorageOptions) string {
	install := shellJoin(remoteCatchInstallCommand(userAtRemote, false, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags, prepareVMLANBridge, skipVMLANBridge, storage))
	logPath := shellQuote(session.LogPath)
	statusPath := shellQuote(session.StatusPath)
	body := fmt.Sprintf("%s >%s 2>&1; code=$?; printf \"%%s\" \"$code\" >%s", install, logPath, statusPath)
	return fmt.Sprintf(
		"rm -f %s %s; nohup sh -c %s >/dev/null 2>&1 </dev/null &",
		logPath,
		statusPath,
		shellQuote(body),
	)
}

func waitDetachedInitCatchInstall(userAtRemote string, session initInstallSession, filter *initInstallFilter) (string, error) {
	deadline := time.Now().Add(initInstallTimeout)
	var lastReadErr error
	var lastLog string
	bridgePrepStarted := false
	for {
		status, err := readRemoteInitInstallFileFn(userAtRemote, session.StatusPath)
		if err != nil {
			lastReadErr = err
		} else if strings.TrimSpace(status) != "" {
			return finishDetachedInitCatchInstall(userAtRemote, session, filter, &lastLog, status)
		} else if err := streamRemoteInitInstallLogFn(userAtRemote, session, filter, &lastLog); err != nil {
			lastReadErr = err
		}
		bridgePrepStarted = bridgePrepStarted || isVMLANBridgePrepLog(lastLog)
		if time.Now().After(deadline) {
			if bridgePrepStarted {
				return "", fmt.Errorf("VM LAN bridge preparation may still be finishing; rerun `yeet init %s` to verify or resume setup", userAtRemote)
			}
			if lastReadErr != nil {
				return "", fmt.Errorf("timed out waiting for catch install to finish: %w", lastReadErr)
			}
			return "", fmt.Errorf("timed out waiting for catch install to finish")
		}
		time.Sleep(initInstallPollInterval)
	}
}

func finishDetachedInitCatchInstall(userAtRemote string, session initInstallSession, filter *initInstallFilter, lastLog *string, status string) (string, error) {
	status = strings.TrimSpace(status)
	if err := streamRemoteInitInstallLogFn(userAtRemote, session, filter, lastLog); err != nil && status != "0" {
		return "", err
	}
	return status, nil
}

func streamRemoteInitInstallLog(userAtRemote string, session initInstallSession, filter *initInstallFilter, lastLog *string) error {
	logRaw, err := readRemoteInitInstallFileFn(userAtRemote, session.LogPath)
	if err != nil {
		return fmt.Errorf("failed to read remote install log: %w", err)
	}
	if logRaw == "" {
		return nil
	}
	delta := logRaw
	if strings.HasPrefix(logRaw, *lastLog) {
		delta = logRaw[len(*lastLog):]
	}
	*lastLog = logRaw
	if delta == "" {
		return nil
	}
	_, err = filter.Write([]byte(delta))
	return err
}

func isVMLANBridgePrepLog(logRaw string) bool {
	return strings.Contains(logRaw, "Preparing VM LAN bridge")
}

func readRemoteInitInstallFile(userAtRemote, path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), initInstallSSHTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", userAtRemote, "cat "+shellQuote(path)+" 2>/dev/null || true")
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func cleanupDetachedInitCatchInstall(userAtRemote string, session initInstallSession) {
	ctx, cancel := context.WithTimeout(context.Background(), initInstallSSHTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", userAtRemote, "rm -f "+shellQuote(session.LogPath)+" "+shellQuote(session.StatusPath))
	_ = cmd.Run()
}

func remoteCatchInstallCommand(userAtRemote string, useSudo bool, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string, prepareVMLANBridge bool, skipVMLANBridge bool, storage initStorageOptions) []string {
	args := []string{}
	if useSudo {
		args = append(args, "sudo")
	}
	installEnv := catchInstallEnvForOptions(userAtRemote, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags, prepareVMLANBridge, skipVMLANBridge, storage)
	if len(installEnv) > 0 {
		args = append(args, "env")
		args = append(args, installEnv...)
	}
	return append(args, catchInstallBinaryArgs(storage)...)
}

func catchInstallEnvForOptions(userAtRemote string, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string, prepareVMLANBridge bool, skipVMLANBridge bool, storage initStorageOptions) []string {
	installEnv := catchInstallEnv(userAtRemote)
	installEnv = append(installEnv, catchStorageEnv(storage)...)
	if installDocker {
		installEnv = append(installEnv, "CATCH_INSTALL_DOCKER=1")
	}
	if installVMTools {
		installEnv = append(installEnv, "CATCH_INSTALL_VM_TOOLS=1")
	}
	if prepareVMLANBridge {
		installEnv = append(installEnv, "CATCH_PREPARE_VM_LAN_BRIDGE=1")
	} else if skipVMLANBridge {
		installEnv = append(installEnv, "CATCH_SKIP_VM_LAN_BRIDGE=1")
	}
	if tsAuthKey != "" {
		installEnv = append(installEnv, "TS_AUTHKEY="+tsAuthKey)
	}
	if tsClientSecret != "" {
		installEnv = append(installEnv, "TS_CLIENT_SECRET="+tsClientSecret)
		if len(tsCatchTags) == 0 {
			tsCatchTags = []string{defaultCatchTag}
		}
		installEnv = append(installEnv, "TS_CATCH_TAGS="+strings.Join(tsCatchTags, ","))
	}
	return installEnv
}

func catchStorageEnv(storage initStorageOptions) []string {
	var env []string
	if storage.DataDirZFS {
		env = append(env, "CATCH_INSTALL_DATA_DIR_ZFS=1")
	}
	if storage.ServicesRootZFS {
		env = append(env, "CATCH_INSTALL_SERVICES_ROOT_ZFS=1")
	}
	return env
}

func catchInstallBinaryArgs(storage initStorageOptions) []string {
	catchArgs := appendCatchStorageArgs([]string{storage.catchRemoteBinary()}, storage)
	catchArgs = append(catchArgs, fmt.Sprintf("--tsnet-host=%v", Host()), "install")
	return catchArgs
}

func catchLocalCommand(storage initStorageOptions, command string) []string {
	catchArgs := appendCatchStorageArgs([]string{storage.catchRemoteBinary()}, storage)
	if env := catchStorageEnv(storage); len(env) > 0 {
		catchArgs = append(append([]string{"env"}, env...), catchArgs...)
	}
	return append(catchArgs, command)
}

func appendCatchStorageArgs(catchArgs []string, storage initStorageOptions) []string {
	if strings.TrimSpace(storage.DataDir) != "" {
		catchArgs = append(catchArgs, fmt.Sprintf("--data-dir=%v", strings.TrimSpace(storage.DataDir)))
	}
	if strings.TrimSpace(storage.ServicesRoot) != "" {
		catchArgs = append(catchArgs, fmt.Sprintf("--services-root=%v", strings.TrimSpace(storage.ServicesRoot)))
	}
	return catchArgs
}

func normalizeInitCatchRemoteBinary(remoteBinary string) string {
	if binary := strings.TrimSpace(remoteBinary); binary != "" {
		return binary
	}
	return "./catch"
}

func remoteCatchInstallArgs(userAtRemote string, useSudo bool, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string, prepareVMLANBridge bool, skipVMLANBridge bool, storage initStorageOptions) []string {
	args := append(make([]string, 0, 8), "-t", userAtRemote)
	return append(args, remoteCatchInstallCommand(userAtRemote, useSudo, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags, prepareVMLANBridge, skipVMLANBridge, storage)...)
}

func catchInstallEnv(userAtRemote string) []string {
	installEnv := []string{}
	if user, host, ok := strings.Cut(userAtRemote, "@"); ok {
		if user != "" {
			installEnv = append(installEnv, "CATCH_INSTALL_USER="+user)
		}
		if host != "" {
			installEnv = append(installEnv, "CATCH_INSTALL_HOST="+host)
		}
	} else if userAtRemote != "" {
		installEnv = append(installEnv, "CATCH_INSTALL_HOST="+userAtRemote)
	}
	return installEnv
}

func removeFileBestEffort(path string) {
	if removeErr := os.Remove(path); removeErr != nil {
		log.Printf("failed to remove temporary catch binary %q: %v", path, removeErr)
	}
}

func localCatchRepoRoot() (string, bool, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", false, nil
	}
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", false, nil
	}
	gitRoot := strings.TrimSpace(string(output))
	if gitRoot == "" {
		return "", false, nil
	}
	if !hasLocalCatchDir(gitRoot) {
		return "", false, nil
	}
	return gitRoot, true, nil
}

func hasLocalCatchDir(gitRoot string) bool {
	info, err := os.Stat(filepath.Join(gitRoot, "cmd", "catch"))
	return err == nil && info.IsDir()
}

func localGoVersion(gitRoot string) (string, error) {
	cmd := exec.Command("go", "version")
	cmd.Dir = gitRoot
	goVersionOut, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go is not installed (required to build catch)")
	}
	return strings.TrimSpace(string(goVersionOut)), nil
}

func preflightSSHHostKey(userAtRemote string) error {
	cmd := exec.Command("ssh", userAtRemote, "true")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh preflight failed: %w", err)
	}
	return nil
}
