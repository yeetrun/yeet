// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
	FromGithub bool `flag:"from-github"`
	Nightly    bool `flag:"nightly"`
}

type initOptions struct {
	fromGithub bool
	nightly    bool
}

func HandleInit(_ context.Context, args []string) error {
	pos, opts, err := parseInitArgs(args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return updateCatch(opts)
	}
	return initCatch(pos[0], opts)
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
		fromGithub: result.Flags.FromGithub,
		nightly:    result.Flags.Nightly,
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
	return initCatch(userAtRemote, opts)
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
	cmd := exec.Command("go", "build", "-o", "catch", "./cmd/catch")
	cmd.Env = append(os.Environ(), "GOARCH="+goarch, "GOOS=linux", "CGO_ENABLED=0")
	cmd.Dir = gitRoot
	return cmd
}

func initCatch(userAtRemote string, opts initOptions) (err error) {
	useSudo := shouldUseSudoForInit(userAtRemote)
	isTTY := isTerminalFn(int(os.Stdout.Fd()))
	enabled, quiet := initProgressSettings(execProgressMode(), isTTY)
	ui := newInitUI(os.Stdout, enabled, quiet, Host(), userAtRemote, catchServiceName)
	ui.Start()
	defer ui.Stop()

	ui.StartStep("Check local")
	source, err := resolveInitCatchSource(opts)
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	ui.DoneStep(source.localDetail())

	if err := verifyInitSSH(ui, userAtRemote); err != nil {
		return err
	}

	systemName, goarch, err := detectInitHost(ui, userAtRemote)
	if err != nil {
		return err
	}

	if source.useGithub {
		if err := downloadInitCatch(ui, userAtRemote, systemName, goarch, opts.nightly); err != nil {
			return err
		}
	} else if err := buildAndUploadInitCatch(ui, userAtRemote, systemName, goarch, source); err != nil {
		return err
	}

	if err := chmodInitCatch(ui, userAtRemote); err != nil {
		return err
	}
	return installInitCatch(ui, userAtRemote, useSudo)
}

type initCatchSource struct {
	useGithub bool
	gitRoot   string
	goVersion string
	reason    string
}

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

func downloadInitCatch(ui *initUI, userAtRemote, systemName, goarch string, nightly bool) error {
	ui.StartStep("Download catch")
	assetName, assetURL, shaURL, tag, err := resolveCatchReleaseAsset(systemName, goarch, nightly)
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

func installInitCatch(ui *initUI, userAtRemote string, useSudo bool) error {
	cmd := exec.Command("ssh", remoteCatchInstallArgs(userAtRemote, useSudo)...)
	cmd.Stdin = os.Stdin
	ui.Suspend()
	filter := newInitInstallFilter(os.Stdout)
	cmd.Stdout = filter
	cmd.Stderr = filter
	if err := cmd.Run(); err != nil {
		ui.FailStep("install failed")
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

func remoteCatchInstallArgs(userAtRemote string, useSudo bool) []string {
	args := append(make([]string, 0, 7), "-t", userAtRemote)
	if useSudo {
		args = append(args, "sudo")
	}
	installEnv := catchInstallEnv(userAtRemote)
	if len(installEnv) > 0 {
		args = append(args, "env")
		args = append(args, installEnv...)
	}
	return append(args, "./catch", fmt.Sprintf("--tsnet-host=%v", Host()), "install")
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
