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
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/shayne/yargs"
	"github.com/yeetrun/yeet/pkg/cmdutil"
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
}

type initOptions struct {
	fromGithub     bool
	nightly        bool
	installDocker  bool
	installVMTools bool
	tsAuthKey      string
	tsClientSecret string
	releaseVersion string
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
	opts := initOptions{
		fromGithub:     result.Flags.FromGithub,
		nightly:        result.Flags.Nightly,
		installDocker:  result.Flags.InstallDocker,
		installVMTools: result.Flags.InstallVMTools,
		tsAuthKey:      result.Flags.TSAuthKey,
		tsClientSecret: result.Flags.TSClientSecret,
	}
	if opts.tsAuthKey != "" && opts.tsClientSecret != "" {
		return nil, initOptions{}, fmt.Errorf("--ts-auth-key and --ts-client-secret cannot be used together")
	}
	if opts.tsClientSecret != "" && !strings.HasPrefix(opts.tsClientSecret, "tskey-client-") {
		return nil, initOptions{}, fmt.Errorf("invalid --ts-client-secret (expected tskey-client-...)")
	}
	if len(pos) > 1 {
		return nil, initOptions{}, fmt.Errorf("init takes at most one argument")
	}
	return pos, opts, nil
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

	if source.useGithub {
		if err := downloadInitCatchFn(ui, userAtRemote, systemName, goarch, opts.nightly, opts.releaseVersion); err != nil {
			return err
		}
	} else if err := buildAndUploadInitCatchFn(ui, userAtRemote, systemName, goarch, source); err != nil {
		return err
	}

	if err := chmodInitCatchFn(ui, userAtRemote); err != nil {
		return err
	}
	return installInitCatchWithTailscaleRetry(ui, userAtRemote, useSudo, installDocker, installVMTools, opts)
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
	prepareInitDockerInstallFn  = prepareInitDockerInstall
	prepareInitVMToolsInstallFn = prepareInitVMToolsInstall
	remoteDockerInstalledFn     = remoteDockerInstalled
	remoteVMHostStatusFn        = remoteVMHostStatus
	confirmInitFn               = cmdutil.Confirm
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
	ok, err := cmdutil.Confirm(os.Stdin, os.Stdout, "Docker is required on the remote host. Install Docker now?")
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
	if warnInitVMHostCapability(ui, status) {
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
	ok, err := confirmInitFn(os.Stdin, os.Stdout, "VM payloads can run on this host, but VM host packages are missing. Install them now?")
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

func warnInitVMHostCapability(ui *initUI, status initVMHostStatus) bool {
	warned := false
	if status.UnsupportedArch != "" {
		ui.Warn(fmt.Sprintf("Warning: VM support is unavailable on this host: yeet VM payloads require x86_64/amd64 hosts in this release; detected %s. See %s", status.UnsupportedArch, hostRequirementsDocsURL))
		warned = true
	}
	if !status.KVM {
		ui.Warn(fmt.Sprintf("Warning: VM support is unavailable on this host: /dev/kvm is missing. Containers, binaries, and cron jobs still work. See %s", hostRequirementsDocsURL))
		warned = true
	}
	if !status.TUN {
		ui.Warn(fmt.Sprintf("Warning: VM networking is unavailable on this host: /dev/net/tun is missing. See %s", hostRequirementsDocsURL))
		warned = true
	}
	return warned
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

func downloadInitCatch(ui *initUI, userAtRemote, systemName, goarch string, nightly bool, version string) error {
	ui.StartStep("Download catch")
	assetName, assetURL, shaURL, tag, err := resolveCatchReleaseAsset(systemName, goarch, nightly, version)
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	downloadDetail, err := downloadCatchRelease(ui, userAtRemote, assetName, assetURL, shaURL)
	if err != nil {
		ui.FailStep("download failed")
		return err
	}
	ui.DoneStep(fmt.Sprintf("%s (%s)", downloadDetail, tag))
	return nil
}

func buildAndUploadInitCatch(ui *initUI, userAtRemote, systemName, goarch string, source initCatchSource) error {
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
	uploadDetail, err := uploadCatchBinary(ui, bin, binSize, userAtRemote)
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

func chmodInitCatch(ui *initUI, userAtRemote string) error {
	ui.StartStep("Install catch")
	cmd := exec.Command("ssh", userAtRemote, "chmod", "+x", "./catch")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		ui.FailStep("chmod failed")
		return fmt.Errorf("failed to make catch binary executable on remote host")
	}
	return nil
}

func installInitCatchWithTailscaleRetry(ui *initUI, userAtRemote string, useSudo bool, installDocker bool, installVMTools bool, opts initOptions) error {
	tsClientSecret := strings.TrimSpace(opts.tsClientSecret)
	for attempt := 0; attempt < 3; attempt++ {
		err := installInitCatchFn(ui, userAtRemote, useSudo, installDocker, installVMTools, opts.tsAuthKey, tsClientSecret, []string{defaultCatchTag})
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
	next, err := promptInitTailscaleClientSecret(os.Stdout, os.Stdin)
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

func promptInitTailscaleClientSecret(out io.Writer, in io.Reader) (string, error) {
	for _, line := range []string{
		"yeet installs catch, a small daemon that runs on the host and manages services.",
		"catch uses Tailscale and must join your tailnet as a tagged device, usually tag:catch.",
		"Paste a Tailscale OAuth client secret with the auth_keys scope that can mint tag:catch.",
		"Recommended: create an owner tag such as tag:yeet, let it own tag:catch, and select tag:yeet on the OAuth client.",
		"Docs: https://yeetrun.com/docs/concepts/tailscale",
		"",
		"Tailscale OAuth client secret:",
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

func installInitCatch(ui *initUI, userAtRemote string, useSudo bool, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string) error {
	if !useSudo {
		return installInitCatchDetached(ui, userAtRemote, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags)
	}
	return installInitCatchDirect(ui, userAtRemote, useSudo, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags)
}

func installInitCatchDirect(ui *initUI, userAtRemote string, useSudo bool, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string) error {
	cmd := exec.Command("ssh", remoteCatchInstallArgs(userAtRemote, useSudo, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags)...)
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
	initInstallPollInterval = 500 * time.Millisecond
	initInstallTimeout      = 5 * time.Minute
	initInstallSSHTimeout   = 10 * time.Second
)

func installInitCatchDetached(ui *initUI, userAtRemote string, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string) error {
	session := newInitInstallSession()
	ui.Suspend()
	filter := newInitInstallFilter(os.Stdout)
	if err := launchDetachedInitCatchInstall(userAtRemote, session, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags); err != nil {
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

func launchDetachedInitCatchInstall(userAtRemote string, session initInstallSession, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string) error {
	cmd := exec.Command("ssh", userAtRemote, detachedInitCatchInstallScript(userAtRemote, session, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags))
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func detachedInitCatchInstallScript(userAtRemote string, session initInstallSession, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string) string {
	install := shellJoin(remoteCatchInstallCommand(userAtRemote, false, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags))
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
	for {
		status, err := readRemoteInitInstallFile(userAtRemote, session.StatusPath)
		if err != nil {
			lastReadErr = err
		} else if strings.TrimSpace(status) != "" {
			if err := streamRemoteInitInstallLog(userAtRemote, session, filter, &lastLog); err != nil {
				return "", err
			}
			return strings.TrimSpace(status), nil
		}
		if err := streamRemoteInitInstallLog(userAtRemote, session, filter, &lastLog); err != nil {
			return "", err
		}
		if time.Now().After(deadline) {
			if lastReadErr != nil {
				return "", fmt.Errorf("timed out waiting for catch install to finish: %w", lastReadErr)
			}
			return "", fmt.Errorf("timed out waiting for catch install to finish")
		}
		time.Sleep(initInstallPollInterval)
	}
}

func streamRemoteInitInstallLog(userAtRemote string, session initInstallSession, filter *initInstallFilter, lastLog *string) error {
	logRaw, err := readRemoteInitInstallFile(userAtRemote, session.LogPath)
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

func remoteCatchInstallCommand(userAtRemote string, useSudo bool, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string) []string {
	args := []string{}
	if useSudo {
		args = append(args, "sudo")
	}
	installEnv := catchInstallEnv(userAtRemote)
	if installDocker {
		installEnv = append(installEnv, "CATCH_INSTALL_DOCKER=1")
	}
	if installVMTools {
		installEnv = append(installEnv, "CATCH_INSTALL_VM_TOOLS=1")
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
	if len(installEnv) > 0 {
		args = append(args, "env")
		args = append(args, installEnv...)
	}
	return append(args, "./catch", fmt.Sprintf("--tsnet-host=%v", Host()), "install")
}

func remoteCatchInstallArgs(userAtRemote string, useSudo bool, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string) []string {
	args := append(make([]string, 0, 7), "-t", userAtRemote)
	return append(args, remoteCatchInstallCommand(userAtRemote, useSudo, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags)...)
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
